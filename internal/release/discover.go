package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// dsoScansResponse is grouped by synchronizer; an SV may appear in several groups.
type dsoScansResponse struct {
	Scans []struct {
		Scans []struct {
			PublicURL string `json:"publicUrl"`
			SvName    string `json:"svName"`
		} `json:"scans"`
	} `json:"scans"`
}

// DiscoverSVInfoURLs derives deduplicated SV /info URLs from a seed scan's
// /api/scan/v0/scans by swapping the "scan." host label for "info.".
//
// Scan endpoints are usually IP-allowlisted, so the seed must be reachable from
// this node (e.g. the validator's own SCAN_ADDRESS); /info endpoints are public.
func DiscoverSVInfoURLs(ctx context.Context, client *http.Client, seedScanURL string) ([]string, error) {
	endpoint := strings.TrimRight(seedScanURL, "/") + "/api/scan/v0/scans"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, endpoint)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	var parsed dsoScansResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse scans JSON: %w", err)
	}

	seen := map[string]bool{}
	var infoURLs []string
	for _, group := range parsed.Scans {
		for _, s := range group.Scans {
			infoURL, ok := scanURLToInfoURL(s.PublicURL)
			if !ok {
				continue
			}
			if seen[infoURL] {
				continue
			}
			seen[infoURL] = true
			infoURLs = append(infoURLs, infoURL)
		}
	}
	return infoURLs, nil
}

// scanURLToInfoURL turns "https://scan.<rest>/path" into "https://info.<rest>".
func scanURLToInfoURL(publicURL string) (string, bool) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return "", false
	}
	if !strings.HasPrefix(u.Host, "scan.") {
		return "", false
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://info." + strings.TrimPrefix(u.Host, "scan."), true
}
