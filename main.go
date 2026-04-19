package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/kkdai/youtube/v2"
	"github.com/o1egl/paseto/v2"
	"golang.org/x/crypto/bcrypt"
)

var (
	dbPool     *pgxpool.Pool
	logger     *slog.Logger
	validate   *validator.Validate
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey
	pv2        = paseto.NewV2()
)

type VidoeInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail"`
	Duration    string `json:"duration"`
	Author      string `json:"author"`
}

type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Username string `json:"username" validate:"required,min=3,max=20"`
	Password string `json:"password" validate:"required,min=8"`
	Accept   bool   `json:"accept" validate:"required"`
}

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=8"`
}

func isValidID(id string) bool {
	return regexp.MustCompile(`^[a-zA-Z0-9_-]{11}$`).MatchString(id)
}

func cleanFileName(title string) string {
	return regexp.MustCompile(`[\\/:*?"<>|]`).ReplaceAllString(title, "")
}

func startCleanupTask() {
	go func() {
		for {
			time.Sleep(30 * time.Minute)
			files, err := os.ReadDir("temp")
			if err != nil {
				continue
			}
			now := time.Now()
			for _, file := range files {
				info, err := file.Info()
				if err != nil {
					continue
				}
				if now.Sub(info.ModTime()) > 1*time.Hour {
					os.Remove(filepath.Join("temp", file.Name()))
				}
			}
		}
	}()
}

func init() {
	_ = godotenv.Load()

	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	if _, err := exec.LookPath("yt-dlp"); err != nil {
		logger.Error("Critical error: yt-dlp not found")
		os.Exit(1)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		logger.Error("Critical error: ffmpeg not found")
		os.Exit(1)
	}
	os.MkdirAll("temp", 0755)

	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		connString = "postgres://velouser:velopass@localhost:5432/velostream"
	}

	var err error
	dbPool, err = pgxpool.New(context.Background(), connString)
	if err != nil {
		logger.Error("Database connection failed", "error", err)
		os.Exit(1)
	}

	if err := dbPool.Ping(context.Background()); err != nil {
		logger.Error("Database ping failed", "error", err)
		os.Exit(1)
	}

	_, err = dbPool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS users (
			id SERIAL PRIMARY KEY,
			username VARCHAR(50) UNIQUE NOT NULL,
			email VARCHAR(255) UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			accepted_terms_at TIMESTAMP NOT NULL
		);
	`)
	if err != nil {
		logger.Error("Table creation failed", "error", err)
		os.Exit(1)
	}

	validate = validator.New()
	publicKey, privateKey, _ = ed25519.GenerateKey(nil)

	startCleanupTask()
	logger.Info("System initialized successfully")
}

func AuthRequired(c *fiber.Ctx) error {
	token := c.Get("Authorization")
	if token == "" {
		return c.Status(401).JSON(fiber.Map{"error": "Authorization token required"})
	}

	token = strings.TrimPrefix(token, "Bearer ")

	var claims paseto.JSONToken
	if err := pv2.Verify(token, publicKey, &claims, nil); err != nil {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid token"})
	}

	if time.Now().After(claims.Expiration) {
		return c.Status(401).JSON(fiber.Map{"error": "Token expired"})
	}

	c.Locals("user_email", claims.Subject)
	return c.Next()
}

func main() {
	urlCors := os.Getenv("CORS_ORIGIN")
	if urlCors == "" {
		urlCors = "*"
	}

	defer dbPool.Close()

	app := fiber.New()

	app.Use(cors.New(cors.Config{
		AllowOrigins: urlCors,
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, OPTIONS",
	}))

	app.Post("/register", func(c *fiber.Ctx) error {
		var req RegisterRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid JSON structure"})
		}

		if err := validate.Struct(req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Validation failed"})
		}

		if !req.Accept {
			return c.Status(400).JSON(fiber.Map{"error": "Debes aceptar los términos legales"})
		}

		req.Email = strings.ToLower(req.Email)
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)

		query := "INSERT INTO users (username, email, password_hash, accepted_terms_at) VALUES ($1, $2, $3, $4)"
		_, err = dbPool.Exec(context.Background(), query, req.Username, req.Email, string(hash), time.Now())

		if err != nil {
			return c.Status(409).JSON(fiber.Map{"error": "User or email already exists"})
		}

		return c.Status(201).JSON(fiber.Map{"message": "User registered successfully"})
	})

	app.Post("/login", func(c *fiber.Ctx) error {
		var req LoginRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid JSON structure"})
		}
		if err := validate.Struct(req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Validation failed"})
		}
		req.Email = strings.ToLower(req.Email)
		var storedHash string
		err := dbPool.QueryRow(context.Background(), "SELECT password_hash FROM users WHERE email = $1", req.Email).Scan(&storedHash)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": "Invalid credentials"})
		}
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(req.Password)); err != nil {
			return c.Status(401).JSON(fiber.Map{"error": "Invalid credentials"})
		}
		now := time.Now()
		token, err := pv2.Sign(privateKey, paseto.JSONToken{
			Expiration: now.Add(24 * time.Hour),
			Subject:    req.Email,
		}, nil)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Internal server error"})
		}
		return c.JSON(fiber.Map{"token": token})
	})
	app.Get("/validate", func(c *fiber.Ctx) error {
		token := c.Get("Authorization")
		if token == "" {
			return c.Status(401).JSON(fiber.Map{"error": "Authorization token required"})
		}
		if err := pv2.Verify(token, publicKey, nil, nil); err != nil {
			return c.Status(401).JSON(fiber.Map{"error": "Invalid token"})
		}
		return c.Status(200).JSON(fiber.Map{"valid": true})
	})
	downloadRoute := app.Group("/download", AuthRequired)

	downloadRoute.Use(limiter.New(limiter.Config{
		Max:        10,
		Expiration: 2 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.Locals("user_email").(string)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{"error": "Download limit reached"})
		},
	}))

	downloadRoute.Get("/", func(c *fiber.Ctx) error {
		videoID := c.Query("id")
		quality := c.Query("quality")

		if !isValidID(videoID) {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid Video ID"})
		}

		cleanURL := "https://www.youtube.com/watch?v=" + videoID
		titleCmd := exec.Command("yt-dlp", "--get-title", "--no-warnings", cleanURL)
		titleOutput, _ := titleCmd.Output()
		videoTitle := "VeloStream_Video"
		if len(titleOutput) > 0 {
			videoTitle = cleanFileName(strings.TrimSpace(string(titleOutput)))
		}

		var format string
		var extension string = "mp4"

		switch quality {
		case "1080":
			format = "bestvideo[height<=1080][ext=mp4]+bestaudio[ext=m4a]/best[height<=1080][ext=mp4]/best"
		case "720":
			format = "bestvideo[height<=720][ext=mp4]+bestaudio[ext=m4a]/best[height<=720][ext=mp4]/best"
		case "480":
			format = "bestvideo[height<=480][ext=mp4]+bestaudio[ext=m4a]/best[height<=480][ext=mp4]/best"
		case "360":
			format = "bestvideo[height<=360][ext=mp4]+bestaudio[ext=m4a]/best[height<=360][ext=mp4]/best"
		case "mp3":
			format = "bestaudio/best"
			extension = "mp3"
		default:
			format = "bestvideo[height<=720][ext=mp4]+bestaudio[ext=m4a]/best[height<=720][ext=mp4]/best"
		}

		fileName := fmt.Sprintf("temp_%s_%s.%s", videoID, quality, extension)
		tempPath := filepath.Join("temp", fileName)

		args := []string{"--quiet", "--no-warnings", "-f", format}
		if quality == "mp3" {
			args = append(args, "-x", "--audio-format", "mp3")
		} else {
			args = append(args, "--merge-output-format", "mp4")
		}
		args = append(args, "-o", tempPath, "--", cleanURL)

		if err := exec.Command("yt-dlp", args...).Run(); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Video processing failed"})
		}

		defer os.Remove(tempPath)
		return c.Download(tempPath, fmt.Sprintf("%s.%s", videoTitle, extension))
	})
	downloadRoute.Get("/video/info", AuthRequired, func(c *fiber.Ctx) error {
		videoID := c.Query("id")
		if !isValidID(videoID) {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid Video ID"})
		}

		client := youtube.Client{}

		video, err := client.GetVideo(videoID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Failed to retrieve video information: " + err.Error()})
		}

		info := VidoeInfo{
			Title:       video.Title,
			Description: video.Description,
			Thumbnail:   video.Thumbnails[1].URL,
			Duration:    video.Duration.String(),
			Author:      video.Author,
		}

		return c.JSON(info)
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	logger.Info("Server starting", "port", port)
	app.Listen(":" + port)
}
