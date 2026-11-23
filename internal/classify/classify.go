// Package classify decides whether a release is safe to auto-apply or should be
// surfaced to a human.
//
// Implementations must fail safe: on any error or uncertainty return Alert. A
// transient failure returns Alert with a non-nil error so a cache won't memoise it.
package classify

import (
	"context"
	"regexp"
)

// Decision is the outcome of classifying a release.
type Decision int

const (
	Alert Decision = iota // human review required (safe default)
	Auto
)

func (d Decision) String() string {
	if d == Auto {
		return "auto"
	}
	return "alert"
}

// Classifier decides auto vs alert for a release version.
type Classifier interface {
	Classify(ctx context.Context, version string) (decision Decision, reason string, err error)
}

// operatorActionSignals are high-precision phrases meaning a standard validator
// operator must act before upgrading: removed config keys, a new required env var,
// or a documented boot workaround. Kept deliberately narrow to avoid matching
// benign notes (existing env vars, SV-only settings, removed endpoints).
var operatorActionSignals = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`(?i)config(?:uration)?\s+(?:option|key|setting|parameter)s?\b[^.]*?\bremoved\b`),
		"config keys removed — operator must clean config"},
	{regexp.MustCompile(`(?i)\bnew\s+(?:required\s+)?(?:environment variable|env var)s?\b`),
		"new required environment variable"},
	{regexp.MustCompile(`(?i)\bworkaround\b[^.]*?\bset\b|\buntil the next (?:patch|release)\b`),
		"documented boot workaround"},
}

// requiresOperatorAction reports whether notes contain a must-act signal.
func requiresOperatorAction(notes string) (reason string, ok bool) {
	for _, s := range operatorActionSignals {
		if s.re.MatchString(notes) {
			return s.reason, true
		}
	}
	return "", false
}

// Fixed always returns the same decision.
type Fixed Decision

func (f Fixed) Classify(context.Context, string) (Decision, string, error) {
	return Decision(f), "fixed policy", nil
}

// Cached memoises definite verdicts by version; errors are not cached so they
// retry next tick. Not safe for concurrent use (only the tick goroutine calls it).
func Cached(inner Classifier) Classifier {
	return &cachedClassifier{inner: inner, decided: map[string]cachedResult{}}
}

type cachedResult struct {
	decision Decision
	reason   string
}

type cachedClassifier struct {
	inner   Classifier
	decided map[string]cachedResult
}

func (c *cachedClassifier) Classify(ctx context.Context, version string) (Decision, string, error) {
	if r, ok := c.decided[version]; ok {
		return r.decision, r.reason, nil
	}
	d, reason, err := c.inner.Classify(ctx, version)
	if err == nil {
		c.decided[version] = cachedResult{decision: d, reason: reason}
	}
	return d, reason, err
}
