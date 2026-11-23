// Package release determines the desired Canton validator version by polling
// Super Validator /info endpoints and requiring ≥2/3 agreement (by count).
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"
)

// Config controls which SVs are polled and how strong the agreement must be.
type Config struct {
	// SVInfoURLs to poll; if empty, discovered from SeedScanURL on first use.
	SVInfoURLs  []string
	SeedScanURL string
	// Threshold is the fraction of SVs that must agree (default 2/3).
	Threshold  float64
	Components []string
}

// Desired is the outcome of one consensus poll.
type Desired struct {
	HaveConsensus bool
	Version       string
	Components    map[string]bool
	Reason        string // set when there is no consensus
}

// Checker polls SV /info endpoints and derives consensus.
type Checker struct {
	cfg        Config
	client     *http.Client
	discovered []string // cached SV /info set resolved from SeedScanURL (once)
}

func New(cfg Config) *Checker {
	if cfg.Threshold == 0 {
		// Not 0.667: ceil(0.667*6) = 5, but ceil(2.0/3.0*6) = 4.
		cfg.Threshold = 2.0 / 3.0
	}
	return &Checker{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type svInfoResponse struct {
	DeploymentInfo struct {
		SV struct {
			Version string `json:"version"`
		} `json:"sv"`
	} `json:"deploymentInfo"`
}

func (c *Checker) poll(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	var info svInfoResponse
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("parse JSON: %w", err)
	}
	ver := info.DeploymentInfo.SV.Version
	if ver == "" {
		return "", fmt.Errorf("empty sv.version in response")
	}
	return ver, nil
}

// infoURLs returns the configured SV /info list, else the (cached) set discovered from SeedScanURL.
func (c *Checker) infoURLs(ctx context.Context) ([]string, error) {
	if len(c.cfg.SVInfoURLs) > 0 {
		return c.cfg.SVInfoURLs, nil
	}
	if len(c.discovered) > 0 {
		return c.discovered, nil
	}
	if c.cfg.SeedScanURL == "" {
		return nil, fmt.Errorf("no SV info URLs configured")
	}
	urls, err := DiscoverSVInfoURLs(ctx, c.client, c.cfg.SeedScanURL)
	if err != nil {
		return nil, fmt.Errorf("discover SV set from seed %s: %w", c.cfg.SeedScanURL, err)
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("seed scan %s returned no usable scans", c.cfg.SeedScanURL)
	}
	slog.Info("discovered SV /info set from seed scan",
		slog.String("seed", c.cfg.SeedScanURL), slog.Int("count", len(urls)))
	c.discovered = urls
	return urls, nil
}

// Desired polls all SVs and returns the consensus outcome. Unreachable SVs
// count as non-agreeing.
func (c *Checker) Desired(ctx context.Context) (Desired, error) {
	infoURLs, err := c.infoURLs(ctx)
	if err != nil {
		return Desired{}, err
	}
	total := len(infoURLs)

	versionCounts := make(map[string]int, total)

	for _, url := range infoURLs {
		ver, err := c.poll(ctx, url)
		if err != nil {
			slog.Warn("SV poll failed, counting as non-agreeing",
				slog.String("url", url),
				slog.Any("err", err),
			)
			continue
		}
		versionCounts[ver]++
	}

	components := make(map[string]bool, len(c.cfg.Components))
	for _, comp := range c.cfg.Components {
		components[comp] = true
	}

	required := int(math.Ceil(c.cfg.Threshold * float64(total)))
	bestVer := ""
	bestCount := 0
	for ver, count := range versionCounts {
		if count > bestCount {
			bestCount = count
			bestVer = ver
		}
	}

	if bestCount < required {
		return Desired{
			HaveConsensus: false,
			Components:    components,
			Reason: fmt.Sprintf("no 2/3 consensus: best=%q count=%d/%d required=%d",
				bestVer, bestCount, total, required),
		}, nil
	}

	return Desired{
		HaveConsensus: true,
		Version:       bestVer,
		Components:    components,
	}, nil
}
