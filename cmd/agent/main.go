// validator-update-agent keeps a Canton validator's containers on the version
// agreed by ≥2/3 of Super Validators.
//
// Subcommands:
//
//	(none)        run the polling daemon
//	apply VERSION force-apply a version, bypassing classifier and rollout window
//
// -mode selects the rollout engine (docker | kubernetes | auto).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // distroless image has no tzdata

	"github.com/upflam/canton-node-operator/internal/agent"
	"github.com/upflam/canton-node-operator/internal/config"
	"github.com/upflam/canton-node-operator/internal/rollout"
	dockerengine "github.com/upflam/canton-node-operator/internal/rollout/docker"
	"github.com/upflam/canton-node-operator/internal/rollout/kube"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if len(os.Args) > 1 && os.Args[1] == "apply" {
		os.Exit(runApply(os.Args[2:]))
	}

	mode := flag.String("mode", "auto", "rollout engine: auto|docker|kubernetes")
	dryRun := flag.Bool("dry-run", false, "log what would happen without applying any updates")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", slog.Any("err", err))
		os.Exit(1)
	}

	resolved := resolveMode(*mode)
	e, err := buildEngine(resolved)
	if err != nil {
		slog.Error("engine init failed", slog.String("mode", resolved), slog.Any("err", err))
		os.Exit(1)
	}

	if cfg.SVSeedScanURL == "" {
		cfg.SVSeedScanURL = waitForScanURL(e)
		if cfg.SVSeedScanURL == "" { // interrupted before a validator appeared
			slog.Info("shutdown signal received before SV scan URL resolved — exiting")
			os.Exit(0)
		}
	}

	rt, err := agent.Build(cfg, *dryRun)
	if err != nil {
		slog.Error("config error", slog.Any("err", err))
		os.Exit(1)
	}

	agent.Run(rt, e)
}

type envFinder interface {
	FindContainerEnv(key string) (string, bool)
}

// validatorScanAddrEnv is the Splice validator's scan-URL env var, reused as the seed scan.
const validatorScanAddrEnv = "SPLICE_APP_VALIDATOR_SCAN_ADDRESS"

const scanURLRetryInterval = 15 * time.Second

// waitForScanURL retries until a validator container exposes the scan URL, so the
// agent survives starting before the validator. Returns "" on SIGINT/SIGTERM.
func waitForScanURL(e rollout.Engine) string {
	f, ok := e.(envFinder)
	if !ok {
		slog.Error("SV_SEED_SCAN_URL is not set and this engine cannot auto-detect it — set SV_SEED_SCAN_URL in .env")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	for {
		if url, found := f.FindContainerEnv(validatorScanAddrEnv); found {
			slog.Info("auto-detected SV scan URL from validator container", slog.String("url", url))
			return url
		}
		slog.Info("SV_SEED_SCAN_URL not set and no validator container found yet — waiting",
			slog.Duration("retry", scanURLRetryInterval))
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(scanURLRetryInterval):
		}
	}
}

func autoDetectScanURL(e rollout.Engine) string {
	f, ok := e.(envFinder)
	if !ok {
		return ""
	}
	url, found := f.FindContainerEnv(validatorScanAddrEnv)
	if !found {
		return ""
	}
	slog.Info("auto-detected SV scan URL from validator container", slog.String("url", url))
	return url
}

func runApply(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: apply VERSION")
		return 1
	}
	version := args[0]

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", slog.Any("err", err))
		return 1
	}

	e, err := buildEngine(resolveMode("auto"))
	if err != nil {
		slog.Error("engine init failed", slog.Any("err", err))
		return 1
	}

	if cfg.SVSeedScanURL == "" {
		cfg.SVSeedScanURL = autoDetectScanURL(e)
	}

	rt, err := agent.Build(cfg, false)
	if err != nil {
		slog.Error("config error", slog.Any("err", err))
		return 1
	}

	if err := agent.ApplyForced(context.Background(), rt, e, version); err != nil {
		slog.Error("apply failed", slog.Any("err", err))
		return 1
	}
	return 0
}

func resolveMode(mode string) string {
	if mode != "auto" {
		return mode
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "kubernetes"
	}
	return "docker"
}

func buildEngine(mode string) (rollout.Engine, error) {
	switch mode {
	case "docker":
		return dockerengine.New()
	case "kubernetes":
		return kube.New()
	default:
		return nil, fmt.Errorf("unknown -mode %q (want auto|docker|kubernetes)", mode)
	}
}
