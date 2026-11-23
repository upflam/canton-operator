package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAlert_PostsToWebhook(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
	}))
	defer srv.Close()

	New(srv.URL).Alert("update %s failed", "validator-app")

	select {
	case body := <-got:
		var payload map[string]string
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("bad JSON body %q: %v", body, err)
		}
		if !strings.Contains(payload["text"], "[validator-update-agent]") ||
			!strings.Contains(payload["text"], "update validator-app failed") {
			t.Fatalf("unexpected text: %q", payload["text"])
		}
	default:
		t.Fatal("webhook was not called")
	}
}

func TestAlert_EmptyWebhookSkips(t *testing.T) {
	// No webhook configured: must not panic and must not attempt any request.
	New("").Alert("just logged %d", 1)
}

func TestAlertBlocks_PostsTextAndBlocks(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
	}))
	defer srv.Close()

	blocks := []any{map[string]any{"type": "section"}}
	New(srv.URL).AlertBlocks("core: 0.9.0 requires attention", blocks)

	select {
	case body := <-got:
		var payload map[string]any
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("bad JSON body %q: %v", body, err)
		}
		text, _ := payload["text"].(string)
		if !strings.Contains(text, "[validator-update-agent]") || !strings.Contains(text, "requires attention") {
			t.Fatalf("unexpected text: %q", text)
		}
		if _, ok := payload["blocks"]; !ok {
			t.Fatal("expected 'blocks' field in payload")
		}
	default:
		t.Fatal("webhook was not called")
	}
}

func TestAlertBlocks_EmptyWebhookSkips(t *testing.T) {
	New("").AlertBlocks("core: 0.9.0 requires attention", nil)
}
