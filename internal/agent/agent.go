package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/upflam/canton-node-operator/internal/classify"
	"github.com/upflam/canton-node-operator/internal/config"
	"github.com/upflam/canton-node-operator/internal/notify"
	"github.com/upflam/canton-node-operator/internal/release"
	"github.com/upflam/canton-node-operator/internal/rollout"
	slackh "github.com/upflam/canton-node-operator/internal/slack"
	"github.com/upflam/canton-node-operator/internal/window"
)

// Lower runs first; anything unlisted runs last (immediately, no ordering constraint).
var updatePriority = map[string]int{
	"canton-participant": 0,
	"validator-app":      1,
}

func priorityOf(short string) int {
	if p, ok := updatePriority[short]; ok {
		return p
	}
	return 2
}

// Group is a set of components that share a target-version source.
type Group struct {
	Name       string
	Components map[string]bool
	Resolver   release.VersionResolver
	Classifier classify.Classifier
	NotesURL   string // linked in Slack alerts (may be empty)
}

type Config struct {
	Webhook            string
	Interval           time.Duration
	DryRun             bool
	Window             window.Window
	Groups             []Group
	SlackAppToken      string
	AIClassifierURL    string
	AIClassifierAPIKey string
}

const (
	coreNotesURL          = "https://docs.canton.network/global-synchronizer/release-notes/splice"
	walletGatewayTagsURL  = "https://ghcr.io/v2/digital-asset/wallet-gateway/docker/wallet-gateway/tags/list"
	walletGatewayNotesURL = "https://docs.canton.network/integrations/release-notes/wallet-gateway-releases"
	portfolioTagsURL      = "https://ghcr.io/v2/digital-asset/splice-portfolio/docker/splice-portfolio/tags/list"

	alertDedupInterval = 30 * time.Minute
	autoApproveAfter   = 48 * time.Hour

	// minPollInterval protects SV /info endpoints from a mistyped POLL_INTERVAL.
	// Dry-run bypasses it.
	minPollInterval = 5 * time.Minute
)

// alertState tracks per-(group, version) alert dedup and the 48h auto-approval.
type alertState struct {
	now          func() time.Time
	mu           sync.Mutex
	lastAlertAt  map[string]time.Time
	firstAlertAt map[string]time.Time
}

func newAlertState() *alertState {
	return &alertState{
		now:          time.Now,
		lastAlertAt:  make(map[string]time.Time),
		firstAlertAt: make(map[string]time.Time),
	}
}

// recordManualReview returns autoApprove once 48h have passed since the first
// alert, and send when the dedup interval has elapsed since the last one.
func (s *alertState) recordManualReview(key string) (send, autoApprove bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	first, ok := s.firstAlertAt[key]
	if !ok {
		s.firstAlertAt[key] = now
		first = now
	}
	if now.Sub(first) >= autoApproveAfter {
		return false, true
	}
	last := s.lastAlertAt[key]
	if now.Sub(last) >= alertDedupInterval {
		s.lastAlertAt[key] = now
		return true, false
	}
	return false, false
}

// Build assembles the runtime Config.
func Build(cfg *config.Config, dryRun bool) (*Config, error) {
	groups, err := buildGroups(cfg)
	if err != nil {
		return nil, err
	}
	interval := cfg.Interval
	if !dryRun && interval < minPollInterval {
		slog.Warn("POLL_INTERVAL below minimum; clamping",
			slog.String("requested", interval.String()),
			slog.String("min", minPollInterval.String()))
		interval = minPollInterval
	}
	return &Config{
		Webhook:            cfg.Webhook,
		Interval:           interval,
		DryRun:             dryRun,
		Window:             cfg.Window,
		Groups:             groups,
		SlackAppToken:      cfg.SlackAppToken,
		AIClassifierURL:    cfg.AIClassifierURL,
		AIClassifierAPIKey: cfg.AIClassifierAPIKey,
	}, nil
}

func buildGroups(cfg *config.Config) ([]Group, error) {
	newClassifier, err := classifierFactory(cfg)
	if err != nil {
		return nil, err
	}

	checker := release.New(release.Config{
		SeedScanURL: cfg.SVSeedScanURL,
		Components:  []string{"canton-participant", "validator-app", "wallet-web-ui", "ans-web-ui"},
	})

	// wallet-gateway and portfolio have no parseable notes page, so they always auto-apply.
	autoFixed := classify.Fixed(classify.Auto)

	// Strip sentences about add-ons that have their own groups from core notes.
	coreExclude := []string{"wallet-gateway", "wallet gateway", "portfolio", "Portfolio UI", "Wallet Gateway"}

	return []Group{
		{
			Name:       "core",
			Components: map[string]bool{"canton-participant": true, "validator-app": true, "wallet-web-ui": true, "ans-web-ui": true},
			Resolver:   release.NewConsensusResolver(checker),
			Classifier: newClassifier(classify.SpliceScheme(coreNotesURL), coreExclude...),
			NotesURL:   coreNotesURL,
		},
		{
			Name:       "wallet-gateway",
			Components: map[string]bool{"wallet-gateway": true},
			Resolver:   release.NewRegistryResolver(walletGatewayTagsURL),
			Classifier: autoFixed,
			NotesURL:   walletGatewayNotesURL,
		},
		{
			Name:       "portfolio",
			Components: map[string]bool{"splice-portfolio": true},
			Resolver:   release.NewRegistryResolver(portfolioTagsURL),
			Classifier: autoFixed,
		},
	}, nil
}

func classifierFactory(cfg *config.Config) (func(classify.NotesScheme, ...string) classify.Classifier, error) {
	switch cfg.UpdateMode {
	case "always":
		return func(classify.NotesScheme, ...string) classify.Classifier { return classify.Fixed(classify.Auto) }, nil
	case "ai":
		slog.Info("ai classifier initialised", slog.String("url", func() string {
			if cfg.AIClassifierURL != "" {
				return cfg.AIClassifierURL
			}
			return "http://localhost:8000"
		}()))
		return func(s classify.NotesScheme, excludeTerms ...string) classify.Classifier {
			return classify.Cached(classify.NewLaya(cfg.AIClassifierURL, cfg.AIClassifierAPIKey, s, excludeTerms...))
		}, nil
	default:
		return nil, fmt.Errorf("unknown UPDATE_MODE %q: use \"always\" (default) or \"ai\"", cfg.UpdateMode)
	}
}

func Run(cfg *Config, e rollout.Engine) {
	n := notify.New(cfg.Webhook)
	state := newAlertState()

	// A signal mid-tick lets the tick finish so a recreate is never interrupted.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.SlackAppToken != "" {
		go func() {
			if err := slackh.Run(ctx, cfg.SlackAppToken, func(ctx context.Context, version string) error {
				return ApplyForced(ctx, cfg, e, version)
			}); err != nil && ctx.Err() == nil {
				slog.Error("socket mode stopped", slog.Any("err", err))
			}
		}()
	}

	groupNames := make([]string, len(cfg.Groups))
	for i, g := range cfg.Groups {
		groupNames[i] = g.Name
	}
	slog.Info("agent started",
		slog.String("engine", e.Kind()),
		slog.String("groups", strings.Join(groupNames, ",")),
		slog.String("interval", cfg.Interval.String()),
		slog.String("rolloutWindow", cfg.Window.String()),
		slog.Bool("dryRun", cfg.DryRun),
	)
	if cfg.Webhook == "" {
		slog.Info("slack: SLACK_WEBHOOK_URL not set — alerts will be logged only")
	}

	for {
		if err := tick(ctx, cfg, n, e, state); err != nil {
			slog.Error("tick error", slog.Any("err", err))
		}
		select {
		case <-ctx.Done():
			slog.Info("shutdown signal received — exiting")
			return
		case <-time.After(cfg.Interval):
		}
	}
}

func tick(ctx context.Context, cfg *Config, n *notify.Notifier, e rollout.Engine, state *alertState) error {
	for _, g := range cfg.Groups {
		if err := updateGroupOnce(ctx, cfg, n, e, g, state); err != nil {
			return err
		}
	}
	return nil
}

func updateGroupOnce(ctx context.Context, cfg *Config, n *notify.Notifier, e rollout.Engine, g Group, state *alertState) error {
	res, err := g.Resolver.Resolve(ctx)
	if err != nil {
		return fmt.Errorf("%s: resolve version: %w", g.Name, err)
	}

	workloads, err := e.Managed(g.Components)
	if err != nil {
		return fmt.Errorf("%s: discovery: %w", g.Name, err)
	}
	if len(workloads) == 0 {
		return nil
	}

	sort.SliceStable(workloads, func(i, j int) bool {
		return priorityOf(workloads[i].Component) < priorityOf(workloads[j].Component)
	})

	target := rollout.Target{Version: res.Version}

	if res.Skip {
		var pendingCount int
		if res.Version != "" {
			for _, w := range workloads {
				if need, err := e.NeedsUpdate(w, target); err == nil && need {
					pendingCount++
				}
			}
		}
		if pendingCount > 0 {
			slog.Info(fmt.Sprintf("%s: waiting for consensus on %s — %d container(s) pending", g.Name, res.Version, pendingCount))
		} else {
			slog.Info(fmt.Sprintf("%s: %s", g.Name, res.Reason))
		}
		return nil
	}

	var pending []rollout.Workload
	for _, w := range workloads {
		need, err := e.NeedsUpdate(w, target)
		if err != nil {
			return fmt.Errorf("%s: needs-update check (%s): %w", g.Name, w.Component, err)
		}
		if need {
			pending = append(pending, w)
		}
	}
	if len(pending) == 0 {
		slog.Info(fmt.Sprintf("%s: all containers at %s", g.Name, target.Version))
		return nil
	}

	upToDate := len(workloads) - len(pending)
	if upToDate > 0 {
		slog.Info(fmt.Sprintf("%s: %s — updating %d, %d already current", g.Name, res.Version, len(pending), upToDate))
	} else {
		slog.Info(fmt.Sprintf("%s: %s — updating %d container(s)", g.Name, res.Version, len(pending)))
	}

	if !cfg.Window.Open(time.Now()) {
		slog.Info(fmt.Sprintf("%s: %s deferred — outside rollout window", g.Name, res.Version))
		return nil
	}

	classifier := g.Classifier
	if classifier == nil {
		classifier = classify.Fixed(classify.Auto)
	}
	decision, _, err := classifier.Classify(ctx, res.Version)
	if err != nil {
		slog.Warn(fmt.Sprintf("%s: classifier error — defaulting to alert", g.Name), slog.Any("err", err))
		decision = classify.Alert
	}

	if decision != classify.Auto {
		key := "review:" + g.Name + ":" + res.Version
		send, autoApprove := state.recordManualReview(key)
		if autoApprove {
			slog.Info(fmt.Sprintf("%s: auto-applying %s — 48 hours elapsed with no manual action", g.Name, res.Version))
		} else {
			if send {
				if cfg.SlackAppToken != "" {
					n.AlertBlocks(
						fmt.Sprintf("%s: %s requires attention before upgrading", g.Name, res.Version),
						buildApproveBlocks(g.Name, res.Version, g.NotesURL),
					)
				} else {
					n.Alert("%s", formatManualReviewAlert(g.Name, res.Version, g.NotesURL))
				}
			}
			return nil
		}
	}

	var applied []string
	for _, w := range pending {
		recreated, err := applyOne(cfg, e, w, target)
		if err != nil {
			n.Alert("update %s (%s) FAILED: %v — please investigate (NO rollback)", w.Component, w.Name, err)
			// A core failure blocks the rest of the cycle.
			if w.Component == "validator-app" || w.Component == "canton-participant" {
				return fmt.Errorf("stopping: %s update failed", w.Component)
			}
			continue
		}
		if recreated {
			applied = append(applied, w.Component)
		}
	}

	if len(applied) > 0 && !cfg.DryRun {
		n.Notify("%s", formatUpdatedNotice(g.Name, res.Version, applied, g.NotesURL))
	}
	return nil
}

func applyOne(cfg *Config, e rollout.Engine, w rollout.Workload, target rollout.Target) (bool, error) {
	slog.Info(fmt.Sprintf("updating %s to %s", w.Component, target.Version))
	if cfg.DryRun {
		slog.Info(fmt.Sprintf("[dry-run] would pull, retag :auto, recreate %s (%s)", w.Component, w.Name))
		return false, nil
	}

	recreated, err := e.Apply(w, target)
	if err != nil {
		return false, err
	}
	if recreated {
		slog.Info(fmt.Sprintf("%s updated to %s", w.Component, target.Version))
	} else {
		slog.Info(fmt.Sprintf("%s synced — image unchanged, no recreate", w.Component))
	}
	return recreated, nil
}

func formatUpdatedNotice(group, version string, components []string, notesURL string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%s* has been auto-updated to %s.", group, version)
	fmt.Fprintf(&sb, "\n\nUpdated: %s", strings.Join(components, ", "))
	if notesURL != "" {
		fmt.Fprintf(&sb, "\n\nRelease notes:\n%s", notesURL)
	}
	return sb.String()
}

func formatManualReviewAlert(group, version, notesURL string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%s* version %s may require attention before upgrading.", group, version)
	if notesURL != "" {
		fmt.Fprintf(&sb, "\n\nCheck the release notes:\n%s", notesURL)
	}
	fmt.Fprintf(&sb, "\n\nAfter reviewing, approve with:\n  docker compose exec splice-updater apply %s", version)
	fmt.Fprintf(&sb, "\n\nWill auto-upgrade in 48 hours if no action is taken.")
	return sb.String()
}

func buildApproveBlocks(group, version, notesURL string) []any {
	text := fmt.Sprintf("*%s* version %s may require attention before upgrading.", group, version)
	if notesURL != "" {
		text += "\n\nCheck the release notes:\n" + notesURL
	}
	contextText := fmt.Sprintf("Will auto-upgrade in 48 hours if no action taken. CLI: `docker compose exec splice-updater apply %s`", version)
	return []any{
		map[string]any{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": text},
		},
		map[string]any{
			"type": "actions",
			"elements": []any{
				map[string]any{
					"type":      "button",
					"text":      map[string]any{"type": "plain_text", "text": "Approve upgrade"},
					"style":     "primary",
					"action_id": "approve_upgrade",
					"value":     version,
				},
			},
		},
		map[string]any{
			"type": "context",
			"elements": []any{
				map[string]any{"type": "mrkdwn", "text": contextText},
			},
		},
	}
}

// ApplyForced applies version to every group resolving to it, bypassing the
// classifier and rollout window.
func ApplyForced(ctx context.Context, cfg *Config, e rollout.Engine, version string) error {
	n := notify.New(cfg.Webhook)
	var applied, skipped int
	for _, g := range cfg.Groups {
		res, err := g.Resolver.Resolve(ctx)
		if err != nil {
			slog.Warn(fmt.Sprintf("%s: resolve error — skipping", g.Name), slog.Any("err", err))
			skipped++
			continue
		}
		if res.Version != version {
			slog.Info(fmt.Sprintf("%s: target is %q — skipping", g.Name, res.Version))
			skipped++
			continue
		}

		workloads, err := e.Managed(g.Components)
		if err != nil {
			return fmt.Errorf("%s: discovery: %w", g.Name, err)
		}
		if len(workloads) == 0 {
			slog.Info(fmt.Sprintf("%s: no containers found — skipping", g.Name))
			skipped++
			continue
		}

		sort.SliceStable(workloads, func(i, j int) bool {
			return priorityOf(workloads[i].Component) < priorityOf(workloads[j].Component)
		})

		target := rollout.Target{Version: version}
		for _, w := range workloads {
			need, err := e.NeedsUpdate(w, target)
			if err != nil {
				return fmt.Errorf("%s: needs-update check (%s): %w", g.Name, w.Component, err)
			}
			if !need {
				slog.Info(fmt.Sprintf("%s: %s already at %s", g.Name, w.Component, version))
				continue
			}
			slog.Info(fmt.Sprintf("%s: applying %s to %s", g.Name, version, w.Component))
			recreated, err := e.Apply(w, target)
			if err != nil {
				n.Alert("apply %s (%s) FAILED: %v — no rollback", w.Component, w.Name, err)
				if w.Component == "validator-app" || w.Component == "canton-participant" {
					return fmt.Errorf("stopping: %s apply failed: %w", w.Component, err)
				}
				continue
			}
			if recreated {
				slog.Info(fmt.Sprintf("%s: %s updated to %s", g.Name, w.Component, version))
				applied++
			}
		}
	}
	slog.Info(fmt.Sprintf("apply done — %d container(s) updated, %d group(s) skipped", applied, skipped))
	return nil
}
