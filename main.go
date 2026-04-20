package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/joho/godotenv"
	"github.com/kkdai/youtube/v2"
)

var (
	logger *slog.Logger
)

type VideoInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail"`
	Duration    string `json:"duration"`
	Author      string `json:"author"`
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

	startCleanupTask()
	logger.Info("System initialized successfully")
}

func main() {
	urlCors := os.Getenv("CORS_ORIGIN")
	if urlCors == "" {
		urlCors = "*"
	}
	app := fiber.New()
	app.Use(cors.New(cors.Config{
		AllowOrigins: urlCors,
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, OPTIONS",
	}))
	downloadRoute := app.Group("/download")

	downloadRoute.Use(limiter.New(limiter.Config{
		Max:        10,
		Expiration: 2 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{"error": "Download limit reached"})
		},
	}))
	downloadRoute.Get("/", func(c *fiber.Ctx) error {
		videoID := c.Query("id")
		quality := c.Query("quality")
		logger.Info("Download request received", "videoID", videoID, "quality", quality, "ip", c.IP())
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
	downloadRoute.Get("/video/info", func(c *fiber.Ctx) error {
		videoID := c.Query("id")
		if !isValidID(videoID) {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid Video ID"})
		}
		logger.Info("Video info request received", "videoID", videoID, "ip", c.IP())
		client := youtube.Client{}

		video, err := client.GetVideo(videoID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Failed to retrieve video information: " + err.Error()})
		}

		info := VideoInfo{
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
