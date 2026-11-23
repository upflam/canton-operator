package dockerx

import (
	"testing"
)

func TestShortImageName(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/foo/validator-app:auto":                                      "validator-app",
		"ghcr.io/foo/canton-participant:0.8.3":                                "canton-participant",
		"registry:5000/bar/wallet-gateway:v1.2.0":                            "wallet-gateway",
		"europe-docker.pkg.dev/da-images/public/docker/frontend:0.11.11":     "frontend",
		"nginx:1.27.1": "nginx",
	}
	for in, want := range cases {
		if got := ShortImageName(in); got != want {
			t.Errorf("ShortImageName(%q) = %q, want %q", in, got, want)
		}
	}
}
