package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_MissingSeedScanURL_OK(t *testing.T) {
	// SV_SEED_SCAN_URL is optional: the runner auto-detects it from a validator
	// container when unset, so Load must not error on an empty value.
	os.Unsetenv("SV_SEED_SCAN_URL")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SVSeedScanURL != "" {
		t.Errorf("want empty SVSeedScanURL, got %q", cfg.SVSeedScanURL)
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("SV_SEED_SCAN_URL", "https://scan.example.com")
	t.Setenv("POLL_INTERVAL", "")
	t.Setenv("UPDATE_MODE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Interval != 5*time.Minute {
		t.Errorf("default interval = %v, want 5m", cfg.Interval)
	}
	if cfg.UpdateMode != "always" {
		t.Errorf("default update mode = %q, want always", cfg.UpdateMode)
	}
}

func TestLoad_SlackAppToken_OK(t *testing.T) {
	t.Setenv("SV_SEED_SCAN_URL", "https://scan.example.com")
	t.Setenv("SLACK_APP_TOKEN", "xapp-1-test")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SlackAppToken != "xapp-1-test" {
		t.Errorf("SlackAppToken = %q, want xapp-1-test", cfg.SlackAppToken)
	}
}

func TestLoad_ExplicitValues(t *testing.T) {
	t.Setenv("SV_SEED_SCAN_URL", "https://scan.example.com")
	t.Setenv("POLL_INTERVAL", "30s")
	t.Setenv("SLACK_WEBHOOK_URL", "https://hooks.slack.com/xxx")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", cfg.Interval)
	}
	if cfg.Webhook != "https://hooks.slack.com/xxx" {
		t.Errorf("webhook = %q, want example URL", cfg.Webhook)
	}
}
