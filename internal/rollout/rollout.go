// Package rollout defines the container-runtime abstraction used by the agent.
package rollout

// Workload is a running instance managed by an Engine.
type Workload struct {
	Name      string
	Component string // short image name, e.g. "canton-participant"
	Handle    any    // engine-private
}

type Target struct {
	Version string
	Digest  string
}

type Engine interface {
	Managed(components map[string]bool) ([]Workload, error)
	NeedsUpdate(w Workload, t Target) (bool, error)
	// Apply reports whether the workload was recreated.
	Apply(w Workload, t Target) (bool, error)
	Kind() string
}
