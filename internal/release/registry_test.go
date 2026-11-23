package release

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMaxStableSemver(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"picks highest patch", []string{"0.14.0", "0.14.5", "0.14.4"}, "0.14.5"},
		{"across minors", []string{"0.13.11", "0.14.2", "0.12.21"}, "0.14.2"},
		{"double-digit patch beats single", []string{"0.12.9", "0.12.21", "0.12.10"}, "0.12.21"},
		{"ignores pre-release/snapshot", []string{"0.14.4", "0.14.5-snapshot.123"}, "0.14.4"},
		{"ignores non-semver", []string{"latest", "1.2", "stable", "0.14.4"}, "0.14.4"},
		{"v-prefixed ghcr tags keep the v", []string{"v1.11.0", "v1.11.3", "v1.10.0"}, "v1.11.3"},
		{"mixes v and non-v by value", []string{"1.11.0", "v1.11.3"}, "v1.11.3"},
		{"none usable", []string{"latest", "1.2", "main"}, ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := maxStableSemver(tc.tags)
			if got != tc.want {
				t.Errorf("maxStableSemver(%v) = %q, want %q", tc.tags, got, tc.want)
			}
			if ok != (tc.want != "") {
				t.Errorf("ok = %v, want %v", ok, tc.want != "")
			}
		})
	}
}

func TestRegistryResolver_Resolve(t *testing.T) {
	t.Run("latest tag", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"tags":["0.14.0","0.14.5","0.14.4","0.13.11"]}`)
		}))
		defer srv.Close()

		res, err := NewRegistryResolver(srv.URL).Resolve(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Skip {
			t.Fatalf("want a version, got skip=%v", res.Skip)
		}
		if res.Version != "0.14.5" {
			t.Errorf("version = %q, want 0.14.5", res.Version)
		}
	})

	t.Run("registry down → skip, no error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		res, err := NewRegistryResolver(srv.URL).Resolve(context.Background())
		if err != nil {
			t.Fatalf("registry failure must not error the cycle, got: %v", err)
		}
		if !res.Skip {
			t.Errorf("want Skip on registry failure, got %+v", res)
		}
	})

	t.Run("no usable tags → skip", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"tags":["latest","main"]}`)
		}))
		defer srv.Close()

		res, err := NewRegistryResolver(srv.URL).Resolve(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Skip {
			t.Errorf("want Skip when no stable tags, got %+v", res)
		}
	})

	// GHCR-style: anonymous tags/list → 401 with a Bearer challenge; resolver must
	// fetch a token from the advertised realm and retry.
	t.Run("token registry (GHCR-style) handshake", func(t *testing.T) {
		var base string
		mux := http.NewServeMux()
		mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("scope") == "" {
				t.Errorf("token request missing scope")
			}
			fmt.Fprint(w, `{"token":"anon-token"}`)
		})
		mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer anon-token" {
				w.Header().Set("WWW-Authenticate",
					`Bearer realm="`+base+`/token",service="ghcr.io",scope="repository:owner/img:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"tags":["v1.11.0","v1.11.3","v1.10.0"]}`)
		})
		srv := httptest.NewServer(mux)
		base = srv.URL
		defer srv.Close()

		res, err := NewRegistryResolver(base + "/v2/owner/img/tags/list").Resolve(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Version != "v1.11.3" {
			t.Errorf("version = %q, want v1.11.3", res.Version)
		}
	})
}
