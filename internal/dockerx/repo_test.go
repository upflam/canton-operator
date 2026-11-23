package dockerx

import "testing"

func TestRepoNoTag(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/foo/validator-app:auto":      "ghcr.io/foo/validator-app",
		"ghcr.io/foo/canton-participant:0.8.3": "ghcr.io/foo/canton-participant",
		// a host:port must not be mistaken for a tag
		"registry:5000/bar/wallet-gateway:v1.2.0": "registry:5000/bar/wallet-gateway",
		"registry:5000/bar/wallet-gateway":        "registry:5000/bar/wallet-gateway",
		"nginx:1.27.1":                            "nginx",
		"nginx":                                   "nginx",
	}
	for in, want := range cases {
		if got := RepoNoTag(in); got != want {
			t.Errorf("RepoNoTag(%q) = %q, want %q", in, got, want)
		}
	}
}
