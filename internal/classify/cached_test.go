package classify

import (
	"context"
	"errors"
	"testing"
)

// countingClassifier records how many times Classify ran and returns a scripted result.
type countingClassifier struct {
	calls    int
	decision Decision
	err      error
}

func (c *countingClassifier) Classify(context.Context, string) (Decision, string, error) {
	c.calls++
	return c.decision, "reason", c.err
}

func TestCached_AutoMemoisedPerVersion(t *testing.T) {
	inner := &countingClassifier{decision: Auto}
	c := Cached(inner)

	for i := 0; i < 3; i++ {
		d, _, err := c.Classify(context.Background(), "0.8.5")
		if err != nil || d != Auto {
			t.Fatalf("call %d: got (%v,%v), want (auto,nil)", i, d, err)
		}
	}
	if inner.calls != 1 {
		t.Errorf("inner ran %d times for one version, want 1 (cached)", inner.calls)
	}

	// A different version is a cache miss → inner runs again.
	if _, _, err := c.Classify(context.Background(), "0.8.6"); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Errorf("new version should re-run inner; calls=%d want 2", inner.calls)
	}
}

func TestCached_GenuineAlertMemoised(t *testing.T) {
	inner := &countingClassifier{decision: Alert}
	c := Cached(inner)

	for i := 0; i < 3; i++ {
		d, _, err := c.Classify(context.Background(), "0.8.5")
		if err != nil || d != Alert {
			t.Fatalf("call %d: got (%v,%v), want (alert,nil)", i, d, err)
		}
	}
	if inner.calls != 1 {
		t.Errorf("a definite Alert should be cached; calls=%d want 1", inner.calls)
	}
}

func TestCached_ErrorNotCached(t *testing.T) {
	inner := &countingClassifier{decision: Auto, err: errors.New("boom")}
	c := Cached(inner)

	for i := 0; i < 2; i++ {
		if _, _, err := c.Classify(context.Background(), "0.8.5"); err == nil {
			t.Fatal("want error passed through")
		}
	}
	if inner.calls != 2 {
		t.Errorf("errored result must not be cached; calls=%d want 2", inner.calls)
	}
}
