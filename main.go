package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/joho/godotenv"
	"github.com/kkdai/youtube/v2"
)

var (
	logger         *slog.Logger
	mp3InfoCache   = make(map[string]cachedMP3Info)
	mp3InfoCacheMu sync.RWMutex
)

const mp3InfoCacheTTL = 10 * time.Minute

type cachedMP3Info struct {
	Data      MP3InfoResponse
	ExpiresAt time.Time
}

type VideoInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail"`
	Duration    string `json:"duration"`
	Author      string `json:"author"`
}

type MP3InfoRequest struct {
	ID string `json:"id"`
}

type MP3InfoResponse struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	Type         string `json:"type"`
	MimeType     string `json:"mimeType"`
	Name         string `json:"name"`
	Duration     string `json:"duration"`
	Thumbnail    string `json:"thumbnail"`
	Author       string `json:"author"`
	AudioQuality string `json:"audioQuality"`
	Cached       bool   `json:"cached"`
}

func isValidID(id string) bool {
	return regexp.MustCompile(`^[a-zA-Z0-9_-]{11}$`).MatchString(id)
}

func cleanFileName(title string) string {
	return regexp.MustCompile(`[\\/:*?"<>|]`).ReplaceAllString(title, "")
}

func getBestThumbnail(video *youtube.Video) string {
	if len(video.Thumbnails) == 0 {
		return ""
	}

	best := video.Thumbnails[0]
	bestArea := best.Width * best.Height

	for _, thumb := range video.Thumbnails[1:] {
		area := thumb.Width * thumb.Height
		if area > bestArea {
			best = thumb
			bestArea = area
		}
	}

	return best.URL
}

func getBestAudioFormat(formats youtube.FormatList) *youtube.Format {
	var best *youtube.Format

	for i := range formats {
		f := &formats[i]
		if f.AudioChannels <= 0 {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(f.MimeType), "audio/") {
			continue
		}
		if best == nil || f.Bitrate > best.Bitrate {
			best = f
		}
	}

	if best != nil {
		return best
	}

	audioFormats := formats.WithAudioChannels()
	if len(audioFormats) == 0 {
		return nil
	}

	bestIdx := 0
	for i := 1; i < len(audioFormats); i++ {
		if audioFormats[i].Bitrate > audioFormats[bestIdx].Bitrate {
			bestIdx = i
		}
	}

	return &audioFormats[bestIdx]
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
			Thumbnail:   getBestThumbnail(video),
			Duration:    video.Duration.String(),
			Author:      video.Author,
		}

		return c.JSON(info)
	})
	app.Post("/mp3/info", func(c *fiber.Ctx) error {
		var req MP3InfoRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid body. Expected JSON with id"})
		}

		videoID := strings.TrimSpace(req.ID)
		if !isValidID(videoID) {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid Video ID"})
		}

		logger.Info("MP3 info request received", "videoID", videoID, "ip", c.IP())

		mp3InfoCacheMu.RLock()
		cached, found := mp3InfoCache[videoID]
		mp3InfoCacheMu.RUnlock()
		if found && time.Now().Before(cached.ExpiresAt) {
			res := cached.Data
			res.Cached = true
			return c.JSON(res)
		}

		client := youtube.Client{}
		video, err := client.GetVideo(videoID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Failed to retrieve MP3 metadata"})
		}

		bestAudioFormat := getBestAudioFormat(video.Formats)
		if bestAudioFormat == nil {
			return c.Status(500).JSON(fiber.Map{"error": "No audio format available"})
		}

		mp3URL, err := client.GetStreamURL(video, bestAudioFormat)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Failed to retrieve MP3 URL"})
		}

		if mp3URL == "" {
			return c.Status(500).JSON(fiber.Map{"error": "Empty URL returned by YouTube"})
		}

		title := strings.TrimSpace(video.Title)
		if title == "" {
			title = videoID
		}

		response := MP3InfoResponse{
			ID:           videoID,
			URL:          mp3URL,
			Type:         "audio",
			MimeType:     bestAudioFormat.MimeType,
			Name:         title,
			Duration:     video.Duration.String(),
			Thumbnail:    getBestThumbnail(video),
			Author:       video.Author,
			AudioQuality: bestAudioFormat.AudioQuality,
			Cached:       false,
		}

		mp3InfoCacheMu.Lock()
		mp3InfoCache[videoID] = cachedMP3Info{
			Data:      response,
			ExpiresAt: time.Now().Add(mp3InfoCacheTTL),
		}
		mp3InfoCacheMu.Unlock()

		return c.JSON(response)
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	logger.Info("Server starting", "port", port)
	app.Listen(":" + port)
}
