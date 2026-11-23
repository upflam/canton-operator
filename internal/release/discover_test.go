package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const scansBody = `{
  "scans": [
    {
      "domainId": "sync-1",
      "scans": [
        {"publicUrl": "https://scan.sv-1.global.canton.network.example.com", "svName": "SV1"},
        {"publicUrl": "https://scan.sv-2.global.canton.network.example.com", "svName": "SV2"}
      ]
    },
    {
      "domainId": "sync-2",
      "scans": [
        {"publicUrl": "https://scan.sv-1.global.canton.network.example.com", "svName": "SV1"}
      ]
    }
  ]
}`

func scanServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/scan/v0/scans" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestDiscoverSVInfoURLs_DerivesAndDedupes(t *testing.T) {
	srv := scanServer(t, scansBody, http.StatusOK)
	defer srv.Close()

	urls, err := DiscoverSVInfoURLs(context.Background(), &http.Client{}, srv.URL)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	want := []string{
		"https://info.sv-1.global.canton.network.example.com",
		"https://info.sv-2.global.canton.network.example.com",
	}
	if len(urls) != len(want) {
		t.Fatalf("got %v, want %v (SV1 must dedupe across synchronizers)", urls, want)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Errorf("url[%d] = %q, want %q", i, urls[i], want[i])
		}
	}
}

func TestDiscoverSVInfoURLs_HTTPError(t *testing.T) {
	srv := scanServer(t, "nope", http.StatusForbidden)
	defer srv.Close()

	if _, err := DiscoverSVInfoURLs(context.Background(), &http.Client{}, srv.URL); err == nil {
		t.Fatal("a 403 from the seed scan must be an error")
	}
}

func TestScanURLToInfoURL(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"https://scan.sv-1.global.canton.network.x.com", "https://info.sv-1.global.canton.network.x.com", true},
		{"https://scan.sv-1.x.com/api/scan/v0/scans", "https://info.sv-1.x.com", true},
		{"http://scan.local:8080", "http://info.local:8080", true},
		{"https://not-a-scan.x.com", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := scanURLToInfoURL(c.in)
		if ok != c.wantOK || got != c.want {
			t.Errorf("scanURLToInfoURL(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// TestChecker_infoURLs_Discovery covers the resolution precedence: explicit list wins,
// else discover from the seed and cache, else error when neither is configured.
func TestChecker_infoURLs_Discovery(t *testing.T) {
	// Explicit list wins; seed is never consulted.
	explicit := New(Config{SVInfoURLs: []string{"https://info.a"}, SeedScanURL: "http://unused"})
	if got, err := explicit.infoURLs(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("explicit list should be used verbatim, got %v err %v", got, err)
	}

	// Neither list nor seed → error (preserves the no-config contract).
	if _, err := New(Config{}).infoURLs(context.Background()); err == nil {
		t.Fatal("no URLs and no seed must error")
	}

	// Seed discovery populates and caches the set.
	scan := scanServer(t, scansBody, http.StatusOK)
	defer scan.Close()
	c := New(Config{SeedScanURL: scan.URL})
	got, err := c.infoURLs(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("discovery should yield 2 info URLs, got %v err %v", got, err)
	}
	// Cache: after the seed goes away, the resolved set is still returned.
	scan.Close()
	if cached, err := c.infoURLs(context.Background()); err != nil || len(cached) != 2 {
		t.Fatalf("second call should use cached set, got %v err %v", cached, err)
	}
}
