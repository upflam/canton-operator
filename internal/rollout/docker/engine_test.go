package docker

import (
	"time"

	"testing"

	"github.com/upflam/canton-node-operator/internal/dockerx"
	"github.com/upflam/canton-node-operator/internal/rollout"
)

// fakeDocker simulates the Docker image store and records recreates. PullAndRetag
// moves the shared repo:auto tag to the pulled version, mirroring the real daemon.
type fakeDocker struct {
	images    map[string]string // ref -> image id
	recreated []string          // container names recreated, in order
}

func (f *fakeDocker) ImageExists(ref string) bool { _, ok := f.images[ref]; return ok }
func (f *fakeDocker) ImageID(ref string) string   { return f.images[ref] }

func (f *fakeDocker) PullAndRetag(repo, ver, _ string) error {
	id := "sha-" + ver
	f.images[repo+":"+ver] = id
	f.images[repo+":auto"] = id
	return nil
}

func (f *fakeDocker) Recreate(cont dockerx.Container, _ string) (string, error) {
	f.recreated = append(f.recreated, cont.Name)
	return "new-" + cont.Name, nil
}

func (f *fakeDocker) WaitHealthy(string, time.Duration) error               { return nil }
func (f *fakeDocker) Discover(map[string]bool) ([]dockerx.Container, error) { return nil, nil }
func (f *fakeDocker) EnsureAutoTag(dockerx.Container) (string, error)       { return "", nil }
func (f *fakeDocker) FindContainerEnv(string) (string, bool)                { return "", false }

// Two validators on one host share the same image and the same local :auto tag.
// Updating the first moves that shared tag to the target; the second must still be
// detected as needing the update and must still be recreated.
func TestTwoValidatorsOnOneHost_SecondNotSkipped(t *testing.T) {
	const (
		repo    = "ghcr.io/foo/validator-app"
		oldID   = "sha-0.10.1"
		version = "0.10.2"
	)
	f := &fakeDocker{images: map[string]string{repo + ":auto": oldID}}
	e := &Engine{client: f, verifiedAt: map[string]string{}}

	mk := func(name string) rollout.Workload {
		c := dockerx.Container{ID: name, Name: name, Image: repo + ":auto", RunningImageID: oldID}
		return rollout.Workload{Name: name, Component: "validator-app", Handle: c}
	}
	first, second := mk("devnet-validator"), mk("camellia-validator")
	target := rollout.Target{Version: version}

	for _, w := range []rollout.Workload{first, second} {
		need, err := e.NeedsUpdate(w, target)
		if err != nil {
			t.Fatalf("%s: NeedsUpdate: %v", w.Name, err)
		}
		if !need {
			t.Fatalf("%s: want needsUpdate=true before any apply", w.Name)
		}
	}

	if _, err := e.Apply(first, target); err != nil {
		t.Fatalf("apply first: %v", err)
	}

	need, err := e.NeedsUpdate(second, target)
	if err != nil {
		t.Fatalf("NeedsUpdate second: %v", err)
	}
	if !need {
		t.Fatal("second validator skipped: NeedsUpdate=false after the shared :auto tag moved to the target")
	}

	if _, err := e.Apply(second, target); err != nil {
		t.Fatalf("apply second: %v", err)
	}
	if len(f.recreated) != 2 {
		t.Fatalf("want both validators recreated, got %v", f.recreated)
	}
}

// A container already running the target image is not recreated again.
func TestNeedsUpdate_AlreadyAtTargetIsNoop(t *testing.T) {
	const repo = "ghcr.io/foo/validator-app"
	f := &fakeDocker{images: map[string]string{
		repo + ":auto":   "sha-0.10.2",
		repo + ":0.10.2": "sha-0.10.2",
	}}
	e := &Engine{client: f, verifiedAt: map[string]string{}}

	c := dockerx.Container{ID: "v", Name: "v", Image: repo + ":auto", RunningImageID: "sha-0.10.2"}
	w := rollout.Workload{Name: "v", Component: "validator-app", Handle: c}

	need, err := e.NeedsUpdate(w, rollout.Target{Version: "0.10.2"})
	if err != nil {
		t.Fatalf("NeedsUpdate: %v", err)
	}
	if need {
		t.Fatal("container already at target must not need an update")
	}
}
