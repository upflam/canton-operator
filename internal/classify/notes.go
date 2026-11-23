package classify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

// NotesScheme describes where to find a version's release notes.
type NotesScheme struct {
	baseURL string
}

// SpliceScheme is a single page with per-version anchors like id="0-8-4".
func SpliceScheme(baseURL string) NotesScheme {
	return NotesScheme{baseURL: baseURL}
}

// versionToID converts "v1.11.3" to "1-11-3".
func versionToID(version string) string {
	return strings.ReplaceAll(strings.TrimPrefix(version, "v"), ".", "-")
}

// fetchNotesSection returns the plain-text notes for version, or false if unavailable.
func fetchNotesSection(ctx context.Context, client *http.Client, scheme NotesScheme, version string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme.baseURL, nil)
	if err != nil {
		slog.Warn("classifier: could not build notes request", slog.Any("err", err))
		return "", false
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("classifier: notes fetch failed", slog.Any("err", err))
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("classifier: notes page returned non-200", slog.Int("status", resp.StatusCode))
		return "", false
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("classifier: reading notes body failed", slog.Any("err", err))
		return "", false
	}

	return extractSection(string(raw), version)
}

// extractSection returns the text between the version's anchor and the next one.
func extractSection(html, version string) (string, bool) {
	startMarker := `id="` + versionToID(version) + `"`
	startIdx := strings.Index(html, startMarker)
	if startIdx < 0 {
		return "", false
	}

	nextVersionRe := regexp.MustCompile(`id="\d+-\d+[^"]*"`)
	rest := html[startIdx+len(startMarker):]
	loc := nextVersionRe.FindStringIndex(rest)

	var section string
	if loc != nil {
		section = html[startIdx : startIdx+len(startMarker)+loc[0]]
	} else {
		section = html[startIdx:]
	}

	text := normalizeHTML(section)
	return text, text != ""
}

// normalizeHTML strips tags and collapses whitespace, including zero-width spaces.
func normalizeHTML(html string) string {
	tagRe := regexp.MustCompile(`<[^>]+>`)
	text := tagRe.ReplaceAllString(html, " ")

	var b strings.Builder
	prevSpace := false
	for _, r := range text {
		if unicode.IsSpace(r) || r == '​' {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}
