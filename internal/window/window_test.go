package window

import (
	"testing"
	"time"
)

func at(t *testing.T, hhmm string) time.Time {
	t.Helper()
	tm, err := time.Parse("15:04", hhmm)
	if err != nil {
		t.Fatal(err)
	}
	// anchor to a fixed UTC date; only the time-of-day matters
	return time.Date(2026, 1, 2, tm.Hour(), tm.Minute(), 0, 0, time.UTC)
}

func TestEmptyIsAlwaysOpen(t *testing.T) {
	w, err := Parse("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !w.Open(at(t, "03:00")) || !w.Open(at(t, "15:00")) {
		t.Fatal("empty window must always be open")
	}
}

func TestSameDayWindow(t *testing.T) {
	w, err := Parse("02:00-04:00", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"01:59": false,
		"02:00": true,  // inclusive start
		"03:30": true,
		"04:00": false, // exclusive end
		"05:00": false,
	}
	for hhmm, want := range cases {
		if got := w.Open(at(t, hhmm)); got != want {
			t.Errorf("Open(%s) = %v, want %v", hhmm, got, want)
		}
	}
}

func TestMidnightCrossingWindow(t *testing.T) {
	w, err := Parse("22:00-02:00", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"21:59": false,
		"22:00": true,
		"23:30": true,
		"00:30": true,
		"01:59": true,
		"02:00": false,
		"12:00": false,
	}
	for hhmm, want := range cases {
		if got := w.Open(at(t, hhmm)); got != want {
			t.Errorf("Open(%s) = %v, want %v", hhmm, got, want)
		}
	}
}

func TestInvalidSpecs(t *testing.T) {
	for _, spec := range []string{"0200-0400", "25:00-26:00", "02:00", "aa:bb-cc:dd"} {
		if _, err := Parse(spec, "UTC"); err == nil {
			t.Errorf("Parse(%q) should error", spec)
		}
	}
	if _, err := Parse("02:00-04:00", "Not/AZone"); err == nil {
		t.Error("bad timezone should error")
	}
}
