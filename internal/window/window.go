// Package window implements an optional daily rollout window.
package window

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Window is a daily time range. The zero value is always open.
type Window struct {
	set      bool
	startMin int // minutes since midnight, inclusive
	endMin   int // minutes since midnight, exclusive
	loc      *time.Location
}

// Parse builds a Window from "HH:MM-HH:MM" in IANA zone tz (default UTC).
// Ranges like "22:00-02:00" wrap past midnight.
func Parse(spec, tz string) (Window, error) {
	if strings.TrimSpace(spec) == "" {
		return Window{}, nil
	}
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Window{}, fmt.Errorf("invalid ROLLOUT_WINDOW_TZ %q: %w", tz, err)
	}
	lo, hi, ok := strings.Cut(spec, "-")
	if !ok {
		return Window{}, fmt.Errorf("invalid ROLLOUT_WINDOW %q: want HH:MM-HH:MM", spec)
	}
	start, err := parseHHMM(lo)
	if err != nil {
		return Window{}, fmt.Errorf("invalid ROLLOUT_WINDOW start: %w", err)
	}
	end, err := parseHHMM(hi)
	if err != nil {
		return Window{}, fmt.Errorf("invalid ROLLOUT_WINDOW end: %w", err)
	}
	return Window{set: true, startMin: start, endMin: end, loc: loc}, nil
}

func parseHHMM(s string) (int, error) {
	s = strings.TrimSpace(s)
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	hh, err := strconv.Atoi(h)
	if err != nil || hh < 0 || hh > 23 {
		return 0, fmt.Errorf("bad hour in %q", s)
	}
	mm, err := strconv.Atoi(m)
	if err != nil || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("bad minute in %q", s)
	}
	return hh*60 + mm, nil
}

func (w Window) String() string {
	if !w.set {
		return "always-open"
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d %s",
		w.startMin/60, w.startMin%60, w.endMin/60, w.endMin%60, w.loc)
}

// Open reports whether the window is open at t.
func (w Window) Open(t time.Time) bool {
	if !w.set {
		return true
	}
	m := t.In(w.loc)
	cur := m.Hour()*60 + m.Minute()
	if w.startMin <= w.endMin {
		return cur >= w.startMin && cur < w.endMin
	}
	return cur >= w.startMin || cur < w.endMin
}
