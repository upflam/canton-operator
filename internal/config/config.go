// Package config loads runtime configuration from the environment.
package config

import (
	"log/slog"
	"os"
	"time"

	"github.com/upflam/canton-node-operator/internal/window"
)

type Config struct {
	SVSeedScanURL string
	Webhook       string
	Interval      time.Duration
	Window        window.Window
	SlackAppToken string // enables Socket Mode approve buttons
	UpdateMode    string // "always" or "ai"

	AIClassifierURL    string
	AIClassifierAPIKey string
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	seedScanURL := os.Getenv("SV_SEED_SCAN_URL")

	interval := 5 * time.Minute
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			interval = d
		} else {
			slog.Warn("POLL_INTERVAL is not a valid Go duration; using default",
				slog.String("value", v), slog.String("default", interval.String()))
		}
	}

	win, err := window.Parse(os.Getenv("ROLLOUT_WINDOW"), os.Getenv("ROLLOUT_WINDOW_TZ"))
	if err != nil {
		return nil, err
	}

	mode := os.Getenv("UPDATE_MODE")
	if mode == "" {
		mode = "always"
	}

	return &Config{
		SVSeedScanURL:      seedScanURL,
		Webhook:            os.Getenv("SLACK_WEBHOOK_URL"),
		Interval:           interval,
		Window:             win,
		SlackAppToken:      os.Getenv("SLACK_APP_TOKEN"),
		UpdateMode:         mode,
		AIClassifierURL:    os.Getenv("AI_CLASSIFIER_URL"),
		AIClassifierAPIKey: os.Getenv("AI_CLASSIFIER_API_KEY"),
	}, nil
}
