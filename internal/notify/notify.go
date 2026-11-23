package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Notifier sends one-way alerts to a Slack-compatible webhook ({"text": ...}).
type Notifier struct {
	webhookURL string
}

// New returns a Notifier; an empty webhookURL means log only.
func New(webhookURL string) *Notifier {
	return &Notifier{webhookURL: webhookURL}
}

// Alert reports something needing attention.
func (n *Notifier) Alert(format string, args ...any) {
	n.post(slog.LevelWarn, "[ALERT] ", ":rotating_light:", fmt.Sprintf(format, args...))
}

// Notify reports routine news such as a successful update.
func (n *Notifier) Notify(format string, args ...any) {
	n.post(slog.LevelInfo, "[UPDATED] ", ":white_check_mark:", fmt.Sprintf(format, args...))
}

// AlertBlocks sends a Slack Block Kit message; logSummary is what gets logged.
func (n *Notifier) AlertBlocks(logSummary string, blocks any) {
	slog.Warn("[ALERT] " + logSummary)
	n.postJSON(map[string]any{
		"text":   ":rotating_light: [validator-update-agent] " + logSummary,
		"blocks": blocks,
	})
}

func (n *Notifier) post(level slog.Level, logPrefix, emoji, msg string) {
	slog.Log(context.Background(), level, logPrefix+msg)
	n.postJSON(map[string]string{"text": emoji + " [validator-update-agent] " + msg})
}

func (n *Notifier) postJSON(payload any) {
	if n.webhookURL == "" {
		return
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		slog.Error("slack: build request failed", slog.Any("err", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("slack: send failed", slog.Any("err", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("slack: webhook returned unexpected status", slog.Int("status", resp.StatusCode))
	}
}
