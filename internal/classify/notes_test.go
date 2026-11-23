package classify

import (
	"strings"
	"testing"
)

func TestVersionToID(t *testing.T) {
	cases := map[string]string{
		"0.8.4":   "0-8-4",
		"v1.11.3": "1-11-3",
	}
	for in, want := range cases {
		if got := versionToID(in); got != want {
			t.Errorf("versionToID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractSection_SliceToNextVersion(t *testing.T) {
	html := `<h2 id="0-8-4">Release 0.8.4</h2><p>New required env FOO.</p>` +
		`<h2 id="0-8-3">Release 0.8.3</h2><p>Older notes.</p>`

	got, ok := extractSection(html, "0.8.4")
	if !ok {
		t.Fatal("want section found")
	}
	if !strings.Contains(got, "New required env FOO.") {
		t.Errorf("section missing its own body: %q", got)
	}
	if strings.Contains(got, "Older notes.") {
		t.Errorf("section must stop before the next version: %q", got)
	}
}

func TestExtractSection_LastVersionRunsToEnd(t *testing.T) {
	html := `<h2 id="0-8-4">Release 0.8.4</h2><p>Only section.</p>`
	got, ok := extractSection(html, "0.8.4")
	if !ok {
		t.Fatal("want section found")
	}
	if !strings.Contains(got, "Only section.") {
		t.Errorf("section missing body: %q", got)
	}
}

func TestExtractSection_MissingVersion(t *testing.T) {
	if _, ok := extractSection(`<h2 id="0-8-4">x</h2>`, "9.9.9"); ok {
		t.Error("absent version must return ok=false")
	}
}
