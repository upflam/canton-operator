// Package docker implements rollout.Engine on top of internal/dockerx.
package docker

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/upflam/canton-node-operator/internal/dockerx"
	"github.com/upflam/canton-node-operator/internal/rollout"
)

// dockerClient is the subset of *dockerx.Client the engine uses (faked in tests).
type dockerClient interface {
	Discover(managed map[string]bool) ([]dockerx.Container, error)
	EnsureAutoTag(cont dockerx.Container) (string, error)
	FindContainerEnv(key string) (string, bool)
	ImageExists(ref string) bool
	ImageID(ref string) string
	PullAndRetag(repo, ver, digest string) error
	Recreate(cont dockerx.Container, image string) (string, error)
	WaitHealthy(containerID string, timeout time.Duration) error
}

type Engine struct {
	client dockerClient
	// mu guards verifiedAt: the poll loop and Slack approvals can run concurrently.
	mu         sync.Mutex
	verifiedAt map[string]string // container name → last pull-confirmed version
}

func New() (*Engine, error) {
	c, err := dockerx.New()
	if err != nil {
		return nil, err
	}
	return &Engine{client: c, verifiedAt: map[string]string{}}, nil
}

func (e *Engine) Kind() string { return "docker" }

func (e *Engine) FindContainerEnv(key string) (string, bool) {
	return e.client.FindContainerEnv(key)
}

// Managed returns matching running workloads, ensuring each has an :auto tag.
func (e *Engine) Managed(components map[string]bool) ([]rollout.Workload, error) {
	containers, err := e.client.Discover(components)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}

	out := make([]rollout.Workload, 0, len(containers))
	for _, c := range containers {
		if _, err := e.client.EnsureAutoTag(c); err != nil {
			slog.Warn("ensure :auto tag failed",
				slog.String("container", c.Name),
				slog.Any("err", err),
			)
		}
		out = append(out, rollout.Workload{
			Name:      c.Name,
			Component: dockerx.ShortImageName(c.Image),
			Handle:    c,
		})
	}
	return out, nil
}

func (e *Engine) NeedsUpdate(w rollout.Workload, t rollout.Target) (bool, error) {
	c, ok := w.Handle.(dockerx.Container)
	if !ok {
		return false, fmt.Errorf("NeedsUpdate: unexpected Handle type %T", w.Handle)
	}
	repo := dockerx.RepoNoTag(c.Image)
	targetRef := repo + ":" + t.Version
	if e.client.ImageExists(targetRef) {
		// Compare per-container: the shared :auto tag may already have moved.
		targetID := e.client.ImageID(targetRef)
		upToDate := targetID != "" && c.RunningImageID == targetID
		return !upToDate, nil
	}
	// :version tag absent locally; trust the cache if Apply previously confirmed it.
	e.mu.Lock()
	verified := e.verifiedAt[c.Name]
	e.mu.Unlock()
	return verified != t.Version, nil
}

func (e *Engine) Apply(w rollout.Workload, t rollout.Target) (bool, error) {
	c, ok := w.Handle.(dockerx.Container)
	if !ok {
		return false, fmt.Errorf("Apply: unexpected Handle type %T", w.Handle)
	}
	repo := dockerx.RepoNoTag(c.Image)
	targetRef := repo + ":" + t.Version
	runningID := c.RunningImageID

	if err := e.client.PullAndRetag(repo, t.Version, t.Digest); err != nil {
		return false, err
	}

	e.mu.Lock()
	e.verifiedAt[c.Name] = t.Version
	e.mu.Unlock()

	// Already running the target content (tag was just missing locally).
	if runningID != "" && runningID == e.client.ImageID(targetRef) {
		return false, nil
	}

	newID, err := e.client.Recreate(c, repo+":auto")
	if err != nil {
		return false, err
	}
	return true, e.client.WaitHealthy(newID, 2*time.Minute)
}
