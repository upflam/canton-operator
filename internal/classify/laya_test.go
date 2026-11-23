package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mockLayaServer(t *testing.T, noul float64, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if statusCode != http.StatusOK {
			w.WriteHeader(statusCode)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(layaResponse{
			Answers: map[string]layaAnswer{
				"requires_manual_action": {Type: "noul", Noul: noul},
			},
		})
	}))
}

func TestLaya_AskLaya_Alert(t *testing.T) {
	srv := mockLayaServer(t, 0.92, http.StatusOK)
	defer srv.Close()

	l := &Laya{http: &http.Client{}, baseURL: srv.URL}
	prob, err := l.askLaya(context.Background(), "some notes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prob <= 0.5 {
		t.Errorf("expected prob > 0.5, got %v", prob)
	}
}

func TestLaya_AskLaya_Auto(t *testing.T) {
	srv := mockLayaServer(t, 0.08, http.StatusOK)
	defer srv.Close()

	l := &Laya{http: &http.Client{}, baseURL: srv.URL}
	prob, err := l.askLaya(context.Background(), "some notes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prob > 0.5 {
		t.Errorf("expected prob <= 0.5, got %v", prob)
	}
}

func TestLaya_AskLaya_HTTPError(t *testing.T) {
	srv := mockLayaServer(t, 0, http.StatusInternalServerError)
	defer srv.Close()

	l := &Laya{http: &http.Client{}, baseURL: srv.URL}
	_, err := l.askLaya(context.Background(), "some notes")
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

func TestLaya_Classify_AlertDecision(t *testing.T) {
	notesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h2 id="1-0-0">v1.0.0</h2><p>New required env var REQUIRED_TOKEN must be set.</p>`)
	}))
	defer notesSrv.Close()

	layaSrv := mockLayaServer(t, 0.91, http.StatusOK)
	defer layaSrv.Close()

	l := &Laya{scheme: SpliceScheme(notesSrv.URL), http: &http.Client{}, baseURL: layaSrv.URL}
	d, _, err := l.Classify(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != Alert {
		t.Errorf("expected Alert for p=0.91, got %v", d)
	}
}

func TestLaya_Classify_AutoDecision(t *testing.T) {
	notesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h2 id="1-0-0">v1.0.0</h2><p>Bug fixes and performance improvements.</p>`)
	}))
	defer notesSrv.Close()

	layaSrv := mockLayaServer(t, 0.07, http.StatusOK)
	defer layaSrv.Close()

	l := &Laya{scheme: SpliceScheme(notesSrv.URL), http: &http.Client{}, baseURL: layaSrv.URL}
	d, _, err := l.Classify(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != Auto {
		t.Errorf("expected Auto for p=0.07, got %v", d)
	}
}

func TestLaya_Classify_OperatorAction_EscalatesToAlert(t *testing.T) {
	// Model misses the buried config-removal signal (low noul → Auto); the safety-net
	// rule must escalate to Alert.
	notesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h2 id="1-0-0">v1.0.0</h2><p>Upgrade Canton. The foo and bar `+
			`configuration options have been removed. New metric added.</p>`)
	}))
	defer notesSrv.Close()

	layaSrv := mockLayaServer(t, 0.10, http.StatusOK) // model says Auto
	defer layaSrv.Close()

	l := &Laya{scheme: SpliceScheme(notesSrv.URL), http: &http.Client{}, baseURL: layaSrv.URL}
	d, reason, err := l.Classify(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != Alert {
		t.Errorf("expected Alert (config keys removed), got %v (reason=%q)", d, reason)
	}
}

func TestRequiresOperatorAction(t *testing.T) {
	cases := []struct {
		name  string
		notes string
		want  bool
	}{
		// ALERT signals
		{"config options removed", "The foo and bar configuration options have been removed.", true},
		{"config key removed", "The legacy config key was removed in this release.", true},
		{"new required env var", "This requires the new environment variable VALIDATOR_TOKEN to be set before upgrading.", true},
		{"new env var abbrev", "Operators must add a new required env var before starting.", true},
		{"boot workaround set", "Workaround: set ADDITIONAL_CONFIG_DELAY=30s in your config.", true},
		{"until next patch", "Some nodes fail to boot; set EXTRA_ENABLE=true until the next patch.", true},
		// AUTO distractors that must NOT match (per operator feedback)
		{"existing envs must be equal (0.9.0)", "This requires LEDGER_API_AUTH_AUDIENCE and VALIDATOR_AUTH_AUDIENCE in .env to be equal.", false},
		{"SV-only env, not new (0.7.3)", "Supervalidators must temporarily set the following environment variable ADDITIONAL_CONFIG_VIEW_CHANGE_TIMEOUT.", false},
		{"endpoints removed is not config", "The deprecated /v0/dso endpoint has been removed.", false},
		{"functionality removed is not config", "The deprecated TransferCommand functionality has been fully removed.", false},
		{"added config is not removal", "Added a new optional configuration option with a default.", false},
		{"deprecation planned next release", "This endpoint is planned for removal in the next release.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := requiresOperatorAction(tc.notes); got != tc.want {
				t.Errorf("requiresOperatorAction(%q) = %v, want %v", tc.notes, got, tc.want)
			}
		})
	}
}

func TestLaya_Classify_NotesUnavailable(t *testing.T) {
	notesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer notesSrv.Close()

	layaSrv := mockLayaServer(t, 0.0, http.StatusOK)
	defer layaSrv.Close()

	l := &Laya{scheme: SpliceScheme(notesSrv.URL), http: &http.Client{}, baseURL: layaSrv.URL}
	d, _, err := l.Classify(context.Background(), "v1.0.0")
	if err == nil {
		t.Fatal("notes unavailable is a transient miss — want a non-nil error so it isn't cached")
	}
	if d != Alert {
		t.Errorf("expected fail-safe Alert when notes unavailable, got %v", d)
	}
}

func TestFilterByExcluded_RemovesOptionalComponentSentences(t *testing.T) {
	notes := "This release upgrades Canton. The validator deployment can now also deploy the Wallet Gateway and Portfolio UI. This requires LEDGER_API_AUTH_AUDIENCE and VALIDATOR_AUTH_AUDIENCE to be equal. Standard nodes are unaffected."
	got := filterByExcluded(notes, []string{"wallet gateway", "portfolio"})
	if strings.Contains(strings.ToLower(got), "wallet gateway") {
		t.Errorf("expected wallet gateway sentence to be removed, got: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "portfolio") {
		t.Errorf("expected portfolio sentence to be removed, got: %q", got)
	}
	if !strings.Contains(got, "This release upgrades Canton") {
		t.Errorf("expected unrelated sentence to be kept, got: %q", got)
	}
}

func TestFilterByExcluded_EmptyExcludeReturnsOriginal(t *testing.T) {
	notes := "Some release notes text."
	got := filterByExcluded(notes, nil)
	if got != notes {
		t.Errorf("expected original text, got: %q", got)
	}
}

func TestLaya_Classify_InferenceErrorFallsBackToAlert(t *testing.T) {
	notesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h2 id="1-0-0">v1.0.0</h2><p>Bug fixes.</p>`)
	}))
	defer notesSrv.Close()

	layaSrv := mockLayaServer(t, 0, http.StatusServiceUnavailable)
	defer layaSrv.Close()

	l := &Laya{scheme: SpliceScheme(notesSrv.URL), http: &http.Client{}, baseURL: layaSrv.URL}
	d, _, err := l.Classify(context.Background(), "v1.0.0")
	if err == nil {
		t.Fatal("inference error is a transient miss — want a non-nil error so it isn't cached")
	}
	if d != Alert {
		t.Errorf("expected fail-safe Alert on inference error, got %v", d)
	}
}
