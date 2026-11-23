package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func svInfoHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deploymentInfo":{"sv":{"version":"` + version + `"}}}`))
	}
}

func TestDesired_FullAgreement(t *testing.T) {
	sv1 := httptest.NewServer(svInfoHandler("0.8.3"))
	defer sv1.Close()
	sv2 := httptest.NewServer(svInfoHandler("0.8.3"))
	defer sv2.Close()
	sv3 := httptest.NewServer(svInfoHandler("0.8.3"))
	defer sv3.Close()

	c := New(Config{
		SVInfoURLs: []string{sv1.URL, sv2.URL, sv3.URL},
		Threshold:  2.0 / 3.0,
		Components: []string{"canton-participant", "validator-app"},
	})

	d, err := c.Desired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.HaveConsensus {
		t.Errorf("want consensus, reason: %s", d.Reason)
	}
	if d.Version != "0.8.3" {
		t.Errorf("version = %q, want 0.8.3", d.Version)
	}
	if !d.Components["canton-participant"] || !d.Components["validator-app"] {
		t.Error("components map should contain configured components")
	}
}

func TestDesired_SplitNoConsensus(t *testing.T) {
	// Three-way split: no version is held by ≥2/3 → no consensus.
	sv1 := httptest.NewServer(svInfoHandler("0.8.3"))
	defer sv1.Close()
	sv2 := httptest.NewServer(svInfoHandler("0.9.0"))
	defer sv2.Close()
	sv3 := httptest.NewServer(svInfoHandler("0.10.0"))
	defer sv3.Close()

	c := New(Config{
		SVInfoURLs: []string{sv1.URL, sv2.URL, sv3.URL},
		Threshold:  2.0 / 3.0,
	})

	d, err := c.Desired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.HaveConsensus {
		t.Errorf("want no consensus (split), got version %q", d.Version)
	}
	if d.Reason == "" {
		t.Error("Reason must be set when no consensus")
	}
}

func TestDesired_ExactTwoThirds(t *testing.T) {
	// 2 of 3 agree — exactly 2/3, which must count as consensus.
	sv1 := httptest.NewServer(svInfoHandler("0.8.3"))
	defer sv1.Close()
	sv2 := httptest.NewServer(svInfoHandler("0.8.3"))
	defer sv2.Close()
	sv3 := httptest.NewServer(svInfoHandler("0.9.0"))
	defer sv3.Close()

	c := New(Config{
		SVInfoURLs: []string{sv1.URL, sv2.URL, sv3.URL},
		Threshold:  2.0 / 3.0,
	})

	d, err := c.Desired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.HaveConsensus {
		t.Errorf("2 of 3 SVs should meet the 2/3 threshold, reason: %s", d.Reason)
	}
	if d.Version != "0.8.3" {
		t.Errorf("version = %q, want 0.8.3", d.Version)
	}
}

func TestDesired_UnreachableSVCountsAgainst(t *testing.T) {
	// 3 SVs configured, 2 unreachable, 1 agreeing. The denominator is the configured
	// total (3), not reachable (1), so 1 agreement is below 2/3 → no consensus.
	sv1 := httptest.NewServer(svInfoHandler("0.8.3"))
	defer sv1.Close()

	c := New(Config{
		SVInfoURLs: []string{sv1.URL, "http://127.0.0.1:1", "http://127.0.0.1:1"}, // port 1: always refused
		Threshold:  2.0 / 3.0,
	})

	d, err := c.Desired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.HaveConsensus {
		t.Errorf("unreachable SVs should reduce count below threshold, got consensus on %q", d.Version)
	}
}

func TestDesired_UnreachableSVWithLowerThreshold(t *testing.T) {
	// Same setup but with threshold=0.5: ceil(0.5*3)=2, 2 reachable SVs agree → consensus.
	sv1 := httptest.NewServer(svInfoHandler("0.8.3"))
	defer sv1.Close()
	sv2 := httptest.NewServer(svInfoHandler("0.8.3"))
	defer sv2.Close()

	c := New(Config{
		SVInfoURLs: []string{sv1.URL, sv2.URL, "http://127.0.0.1:1"},
		Threshold:  0.5,
	})

	d, err := c.Desired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.HaveConsensus {
		t.Errorf("2 of 3 agreeing with threshold=0.5 should reach consensus, reason: %s", d.Reason)
	}
	if d.Version != "0.8.3" {
		t.Errorf("version = %q, want 0.8.3", d.Version)
	}
}

func TestDesired_NoURLsConfigured(t *testing.T) {
	c := New(Config{SVInfoURLs: []string{}})
	if _, err := c.Desired(context.Background()); err == nil {
		t.Fatal("empty SVInfoURLs should return an error")
	}
}

func TestDesired_GarbageJSONCountsAsNonAgreeing(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json at all"))
	}))
	defer bad.Close()
	good := httptest.NewServer(svInfoHandler("0.8.3"))
	defer good.Close()

	// 1 valid of 2 responses is below 2/3 → no consensus (garbage JSON counts as non-agreeing).
	c := New(Config{
		SVInfoURLs: []string{good.URL, bad.URL},
		Threshold:  2.0 / 3.0,
	})
	d, err := c.Desired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.HaveConsensus {
		t.Error("garbage JSON response should count as non-agreeing, preventing consensus")
	}
}
