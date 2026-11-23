// Package kube will implement rollout.Engine for Kubernetes (Helm charts via the
// Helm Go SDK). Not built yet — selecting -mode=kubernetes fails clearly for now.
package kube

import (
	"errors"

	"github.com/upflam/canton-node-operator/internal/rollout"
)

// New returns the Kubernetes rollout engine. Not implemented yet.
func New() (rollout.Engine, error) {
	return nil, errors.New("kubernetes engine not implemented yet")
}
