package config

import (
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	TelegramToken       string
	TelegramWebhookURL  string
	MongoDBURI          string
	DatabaseName        string
	GitHubWebhookSecret string
	GitHubClientID      string
	GitHubClientSecret  string
	Port                string
	EncryptionKey       string
}

func Load() *Config {
	_ = godotenv.Load()

	required := []string{
		"TELEGRAM_TOKEN",
		"TELEGRAM_WEBHOOK_URL",
		"MONGODB_URI",
		"GITHUB_CLIENT_ID",
		"GITHUB_CLIENT_SECRET",
		"GITHUB_WEBHOOK_SECRET",
		"ENCRYPTION_KEY",
	}

	var missing []string
	for _, key := range required {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		log.Fatalf("Missing required environment variables: %s", strings.Join(missing, ", "))
	}

	return &Config{
		TelegramToken:       os.Getenv("TELEGRAM_TOKEN"),
		TelegramWebhookURL:  os.Getenv("TELEGRAM_WEBHOOK_URL"),
		MongoDBURI:          os.Getenv("MONGODB_URI"),
		DatabaseName:        getEnv("DATABASE_NAME", "github_bot"),
		GitHubWebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		GitHubClientID:      os.Getenv("GITHUB_CLIENT_ID"),
		GitHubClientSecret:  os.Getenv("GITHUB_CLIENT_SECRET"),
		Port:                getEnv("PORT", "8080"),
		EncryptionKey:       os.Getenv("ENCRYPTION_KEY"),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
