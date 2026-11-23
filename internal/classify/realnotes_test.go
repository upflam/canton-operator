package classify

import (
	"net/http"
	"os"
	"testing"
)

// Real Splice releases labelled from the validator operator's perspective: Alert
// only when the operator must act (new required env var, removed config key, manual
// step, boot workaround), not merely when the release is breaking.

const spliceNotesURL = "https://docs.canton.network/global-synchronizer/release-notes/splice"

// coreExcludeTerms mirrors the exclude list wired in agent.go for the core group.
var coreExcludeTerms = []string{"wallet-gateway", "wallet gateway", "portfolio", "Portfolio UI", "Wallet Gateway"}

// TestLayaRealNotes needs a live classifier:
//
//	AI_CLASSIFIER_URL=http://localhost:8000 go test ./internal/classify/... -run TestLayaRealNotes -v
func TestLayaRealNotes(t *testing.T) {
	baseURL := os.Getenv("AI_CLASSIFIER_URL")
	if baseURL == "" {
		t.Skip("AI_CLASSIFIER_URL not set — skipping live classifier benchmark")
	}

	l := NewLaya(baseURL, os.Getenv("AI_CLASSIFIER_API_KEY"), SpliceScheme(spliceNotesURL), coreExcludeTerms...)
	resp, err := http.Get(baseURL + "/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Skipf("Laya not reachable at %s — skipping", baseURL)
	}
	resp.Body.Close()

	// Only dangerous misses (Alert labelled Auto) fail; false positives are logged.
	var passed, dangerous int
	for _, tc := range realNotesCases {
		got, reason, err := l.Classify(t.Context(), tc.version)
		if err != nil {
			t.Errorf("[%s] classify error: %v", tc.version, err)
			continue
		}
		mark := "PASS"
		if got != tc.want {
			mark = "FP" // safe: Auto labelled Alert
			if tc.want == Alert {
				mark = "DANGER" // unsafe: Alert labelled Auto
				dangerous++
			}
		} else {
			passed++
		}
		t.Logf("[%s] %s  got=%s want=%s  reason=%q", tc.version, mark, got, tc.want, reason)
	}
	t.Logf("result: %d/%d correct, %d dangerous miss(es)", passed, len(realNotesCases), dangerous)
	if dangerous > 0 {
		t.Errorf("%d dangerous miss(es): a release needing operator action was labelled Auto", dangerous)
	}
}

type realNotesCase struct {
	version string
	want    Decision
}

var realNotesCases = []realNotesCase{
	{"0.9.0", Auto},  // optional add-ons (wallet-gateway/portfolio) + AUDIENCE equality (already satisfied); no core action
	{"0.8.4", Auto},  // Canton upgrade only
	{"0.8.3", Auto},  // Canton + base image + bugfix
	{"0.7.1", Auto},  // idempotency bugfix
	{"0.6.14", Auto}, // added optional limit + bugfix
	{"0.6.12", Auto}, // security contexts + metric
	{"0.6.9", Auto},  // optional Helm override + base image
	{"0.8.0", Auto},  // breaking API/Daml, but app-handled + SV vote; validators update normally
	{"0.7.5", Auto},  // new rate-limiting config option WITH a default (no action)
	{"0.7.4", Alert}, // removed config keys — operators who set them must clean config
	{"0.7.3", Auto},  // temporary env var is for Super Validators, not validators
	{"0.7.0", Auto},  // future PG14 EOL warning + API endpoint removal (no immediate action)
	{"0.6.6", Auto},  // deprecated public API endpoints removed (consumers, not validator config)
	{"0.6.2", Auto},  // deprecation notice only (planned for a later release)
	{"0.6.1", Auto},  // "applied like any other upgrade"
	{"0.6.10", Auto}, // optional secretOverrides Helm feature
}
