package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	layaDefaultBaseURL = "http://localhost:8000"
	layaModel          = "laya"
	layaHTTPTimeout    = 30 * time.Second
	// Fits the model's context; operator-action callouts appear early in notes.
	layaMaxNotesChars = 1500
)

// Laya classifies a release by asking the Laya model whether its notes require operator action.
type Laya struct {
	scheme       NotesScheme
	baseURL      string
	apiKey       string // optional
	http         *http.Client
	excludeTerms []string
}

func NewLaya(baseURL, apiKey string, scheme NotesScheme, excludeTerms ...string) *Laya {
	if baseURL == "" {
		baseURL = layaDefaultBaseURL
	}
	return &Laya{
		scheme:       scheme,
		baseURL:      baseURL,
		apiKey:       apiKey,
		http:         &http.Client{Timeout: layaHTTPTimeout},
		excludeTerms: excludeTerms,
	}
}

func (l *Laya) Classify(ctx context.Context, version string) (Decision, string, error) {
	notes, ok := fetchNotesSection(ctx, l.http, l.scheme, version)
	if !ok {
		return Alert, "", fmt.Errorf("release notes unavailable")
	}
	filtered := filterByExcluded(notes, l.excludeTerms)
	if len(filtered) > layaMaxNotesChars {
		filtered = filtered[:layaMaxNotesChars] + " …(truncated)"
	}

	prob, err := l.askLaya(ctx, filtered)
	if err != nil {
		return Alert, "", fmt.Errorf("laya inference: %w", err)
	}

	if prob > 0.5 {
		return Alert, fmt.Sprintf("laya: manual action likely (p=%.2f)", prob), nil
	}
	// Safety net for signals the model misses; uses unfiltered notes and can only escalate.
	if reason, ok := requiresOperatorAction(notes); ok {
		return Alert, reason, nil
	}
	return Auto, fmt.Sprintf("laya: auto-safe (p=%.2f)", prob), nil
}

type layaRequest struct {
	State     string                  `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]layaQuestion `json:"questions"`
}

type layaQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type layaResponse struct {
	Answers map[string]layaAnswer `json:"answers"`
}

type layaAnswer struct {
	Type string  `json:"type"`
	Noul float64 `json:"noul"`
}

// The second sentence is load-bearing: without it, removed config keys score as
// auto (0.7.4: 0.43 vs 0.84).
const layaInstruction = "Does this Canton validator release require the operator to take manual action before upgrading — such as a new required environment variable, a mandatory config change, a manual pre/post-upgrade step, or a documented workaround without which the node fails? Answer true only for standard validator deployments, not for Super Validator operators or optional features."

// filterByExcluded drops sentences mentioning any excluded term.
func filterByExcluded(text string, excluded []string) string {
	if len(excluded) == 0 {
		return text
	}
	parts := strings.Split(text, ". ")
	var kept []string
	for _, part := range parts {
		lower := strings.ToLower(part)
		skip := false
		for _, term := range excluded {
			if strings.Contains(lower, strings.ToLower(term)) {
				skip = true
				break
			}
		}
		if !skip {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, ". ")
}

func (l *Laya) askLaya(ctx context.Context, notes string) (float64, error) {
	body, err := json.Marshal(layaRequest{
		State: notes,
		Model: layaModel,
		Questions: map[string]layaQuestion{
			"requires_manual_action": {
				Type:         "noul",
				Instructions: layaInstruction,
			},
		},
	})
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if l.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+l.apiKey)
	}

	resp, err := l.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	var out layaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}

	a, ok := out.Answers["requires_manual_action"]
	if !ok {
		return 0, fmt.Errorf("missing answer key in response")
	}
	return a.Noul, nil
}
