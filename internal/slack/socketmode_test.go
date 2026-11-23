package slack

import (
	"testing"

	slackapi "github.com/slack-go/slack"
)

func TestDispatchInteractive_ApproveUpgrade(t *testing.T) {
	cb := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeBlockActions,
		ActionCallback: slackapi.ActionCallbacks{
			BlockActions: []*slackapi.BlockAction{
				{ActionID: "approve_upgrade", Value: "0.9.0"},
			},
		},
	}
	if got := dispatchInteractive(cb); got != "0.9.0" {
		t.Fatalf("expected 0.9.0, got %q", got)
	}
}

func TestDispatchInteractive_UnknownAction(t *testing.T) {
	cb := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeBlockActions,
		ActionCallback: slackapi.ActionCallbacks{
			BlockActions: []*slackapi.BlockAction{
				{ActionID: "other_action", Value: "v1.0"},
			},
		},
	}
	if got := dispatchInteractive(cb); got != "" {
		t.Fatalf("expected empty for unknown action, got %q", got)
	}
}

func TestDispatchInteractive_EmptyValue(t *testing.T) {
	cb := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeBlockActions,
		ActionCallback: slackapi.ActionCallbacks{
			BlockActions: []*slackapi.BlockAction{
				{ActionID: "approve_upgrade", Value: ""},
			},
		},
	}
	if got := dispatchInteractive(cb); got != "" {
		t.Fatalf("expected empty for blank value, got %q", got)
	}
}

func TestDispatchInteractive_NonCallbackData(t *testing.T) {
	if got := dispatchInteractive("not a callback"); got != "" {
		t.Fatalf("expected empty for non-callback data, got %q", got)
	}
}
