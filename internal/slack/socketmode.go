package slack

import (
	"context"
	"log/slog"
	"sync"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// Run dispatches "approve_upgrade" clicks over Socket Mode (outbound only) to
// onApprove. Blocks until ctx is cancelled or the connection fails.
func Run(ctx context.Context, appToken string, onApprove func(ctx context.Context, version string) error) error {
	api := slackapi.New("", slackapi.OptionAppLevelToken(appToken))
	client := socketmode.New(api)

	var mu sync.Mutex
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-client.Events:
				if !ok {
					return
				}
				if evt.Type != socketmode.EventTypeInteractive {
					continue
				}
				if evt.Request != nil {
					client.Ack(*evt.Request)
				}
				version := dispatchInteractive(evt.Data)
				if version == "" {
					continue
				}
				slog.Info("slack: approve button clicked — applying " + version)
				go func() {
					mu.Lock()
					defer mu.Unlock()
					if err := onApprove(context.Background(), version); err != nil {
						slog.Error("slack: apply failed", slog.String("version", version), slog.Any("err", err))
					}
				}()
			}
		}
	}()

	return client.RunContext(ctx)
}

// dispatchInteractive returns the approve_upgrade version, or "".
func dispatchInteractive(data any) string {
	cb, ok := data.(slackapi.InteractionCallback)
	if !ok {
		return ""
	}
	if cb.Type != slackapi.InteractionTypeBlockActions {
		return ""
	}
	for _, action := range cb.ActionCallback.BlockActions {
		if action.ActionID == "approve_upgrade" && action.Value != "" {
			return action.Value
		}
	}
	return ""
}
