package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	BotToken           string
	AdminIDs           map[int64]struct{}
	ConcurrentRequests int
	RequestTimeoutSec  int
	UserAgents         []string
}

func Load() Config {
	_ = godotenv.Load()

	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("BOT_TOKEN is required")
	}

	adminIDs := make(map[int64]struct{})
	for _, raw := range strings.Split(os.Getenv("ADMIN_IDS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			log.Printf("invalid ADMIN_IDS value: %s", raw)
			continue
		}
		adminIDs[id] = struct{}{}
	}

	concurrent := intFromEnv("CONCURRENT_REQUESTS", 1)
	timeout := intFromEnv("REQUEST_TIMEOUT", 7)

	return Config{
		BotToken:           botToken,
		AdminIDs:           adminIDs,
		ConcurrentRequests: concurrent,
		RequestTimeoutSec:  timeout,
		UserAgents: []string{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
		},
	}
}

func intFromEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return fallback
	}
	return v
}
