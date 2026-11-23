package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const resolverHTTPTimeout = 15 * time.Second

// RegistryResolver resolves to the highest stable tag in a Docker Registry v2
// tags list, using an anonymous bearer token when the registry requires one.
type RegistryResolver struct {
	tagsURL string // https://host/v2/<repo>/tags/list
	http    *http.Client
}

func NewRegistryResolver(tagsURL string) *RegistryResolver {
	return &RegistryResolver{tagsURL: tagsURL, http: &http.Client{Timeout: resolverHTTPTimeout}}
}

// Resolve skips (rather than errors) on registry failures so the update loop keeps running.
func (r *RegistryResolver) Resolve(ctx context.Context) (Resolution, error) {
	tags, err := r.fetchTags(ctx)
	if err != nil {
		slog.Warn("registry resolver: fetch tags failed", slog.String("url", r.tagsURL), slog.Any("err", err))
		return Resolution{Skip: true, Reason: "registry unreachable"}, nil
	}
	latest, ok := maxStableSemver(tags)
	if !ok {
		return Resolution{Skip: true, Reason: "no stable semver tags"}, nil
	}
	return Resolution{Version: latest}, nil
}

func (r *RegistryResolver) fetchTags(ctx context.Context) ([]string, error) {
	tags, status, challenge, err := r.getTags(ctx, "")
	if err != nil {
		return nil, err
	}
	// Token registries (GHCR) answer 401 with a Bearer challenge; get a token and retry once.
	if status == http.StatusUnauthorized && challenge != "" {
		token, terr := fetchBearerToken(ctx, r.http, challenge)
		if terr != nil {
			return nil, terr
		}
		tags, status, _, err = r.getTags(ctx, token)
		if err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", status, r.tagsURL)
	}
	return tags, nil
}

// getTags returns the tags, status, and WWW-Authenticate header on non-200.
func (r *RegistryResolver) getTags(ctx context.Context, token string) (tags []string, status int, challenge string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.tagsURL, nil)
	if err != nil {
		return nil, 0, "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, resp.Header.Get("WWW-Authenticate"), nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, "", err
	}
	var payload struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, resp.StatusCode, "", fmt.Errorf("parse tags JSON: %w", err)
	}
	return payload.Tags, resp.StatusCode, "", nil
}

var bearerParamRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// fetchBearerToken answers a challenge such as
//
//	Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:owner/img:pull"
func fetchBearerToken(ctx context.Context, client *http.Client, challenge string) (string, error) {
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "", fmt.Errorf("unsupported auth challenge: %q", challenge)
	}
	params := map[string]string{}
	for _, m := range bearerParamRe.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("auth challenge missing realm: %q", challenge)
	}

	q := url.Values{}
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	if params["scope"] != "" {
		q.Set("scope", params["scope"])
	}
	tokenURL := realm
	if enc := q.Encode(); enc != "" {
		tokenURL += "?" + enc
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	// Registries return the token as "token" and/or "access_token".
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("parse token JSON: %w", err)
	}
	if payload.Token != "" {
		return payload.Token, nil
	}
	if payload.AccessToken != "" {
		return payload.AccessToken, nil
	}
	return "", fmt.Errorf("token endpoint returned no token")
}

// maxStableSemver returns the highest [v]x.y.z tag, ignoring non-semver and
// pre-release tags.
func maxStableSemver(tags []string) (string, bool) {
	best := [3]int{-1, -1, -1}
	bestStr := ""
	for _, t := range tags {
		v, ok := parseSemver(t)
		if !ok {
			continue
		}
		if v[0] > best[0] ||
			(v[0] == best[0] && v[1] > best[1]) ||
			(v[0] == best[0] && v[1] == best[1] && v[2] > best[2]) {
			best = v
			bestStr = t
		}
	}
	return bestStr, bestStr != ""
}

func parseSemver(tag string) ([3]int, bool) {
	t := strings.TrimPrefix(tag, "v")
	if strings.ContainsAny(t, "-+") {
		return [3]int{}, false
	}
	parts := strings.Split(t, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}
