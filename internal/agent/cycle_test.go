package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/upflam/canton-node-operator/internal/classify"
	"github.com/upflam/canton-node-operator/internal/notify"
	"github.com/upflam/canton-node-operator/internal/release"
	"github.com/upflam/canton-node-operator/internal/rollout"
	"github.com/upflam/canton-node-operator/internal/window"
)

// webhookRecorder captures the {"text":...} payloads posted to a Slack-style webhook.
func webhookRecorder(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		texts = append(texts, payload.Text)
		mu.Unlock()
	}))
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), texts...)
	}
}

// errClassifier always errors, to exercise the fail-safe (→ alert) path.
type errClassifier struct{}

func (errClassifier) Classify(context.Context, string) (classify.Decision, string, error) {
	return classify.Auto, "", errors.New("model unavailable")
}

// fakeEngine is an in-memory rollout.Engine that records Apply calls.
type fakeEngine struct {
	workloads   []rollout.Workload
	needsUpdate map[string]bool // component -> needs update
	applied     []string        // component names in Apply call order
	applyErr    map[string]error
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		needsUpdate: map[string]bool{},
		applyErr:    map[string]error{},
	}
}

func (f *fakeEngine) Kind() string { return "fake" }

func (f *fakeEngine) Managed(components map[string]bool) ([]rollout.Workload, error) {
	var out []rollout.Workload
	for _, w := range f.workloads {
		if components[w.Component] {
			out = append(out, w)
		}
	}
	return out, nil
}

func (f *fakeEngine) NeedsUpdate(w rollout.Workload, _ rollout.Target) (bool, error) {
	return f.needsUpdate[w.Component], nil
}

func (f *fakeEngine) Apply(w rollout.Workload, _ rollout.Target) (bool, error) {
	f.applied = append(f.applied, w.Component)
	err := f.applyErr[w.Component]
	return err == nil, err
}

func svServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deploymentInfo":{"sv":{"version":"` + version + `"}}}`))
	}))
}

// checkerFor uses threshold 0.5 so 1-of-1 or 2-of-3 reaches consensus.
func checkerFor(t *testing.T, servers ...*httptest.Server) *release.Checker {
	t.Helper()
	urls := make([]string, len(servers))
	for i, s := range servers {
		urls[i] = s.URL
	}
	return release.New(release.Config{
		SVInfoURLs: urls,
		Threshold:  0.5,
		Components: []string{"canton-participant", "validator-app", "wallet-web-ui", "ans-web-ui"},
	})
}

func coreGroup(checker *release.Checker, clf classify.Classifier) Group {
	if clf == nil {
		clf = classify.Fixed(classify.Auto)
	}
	return Group{
		Name:       "core",
		Components: map[string]bool{"canton-participant": true, "validator-app": true, "wallet-web-ui": true, "ans-web-ui": true},
		Resolver:   release.NewConsensusResolver(checker),
		Classifier: clf,
	}
}

func baseCfg() *Config {
	return &Config{
		Interval: time.Hour,
		DryRun:   false,
	}
}

func TestUpdateCycle_ConsensusApplies(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(""), e, coreGroup(checkerFor(t, sv), nil), newAlertState()); err != nil {
		t.Fatalf("updateGroupOnce: %v", err)
	}
	if len(e.applied) != 1 || e.applied[0] != "canton-participant" {
		t.Fatalf("expected Apply for canton-participant, got %v", e.applied)
	}
}

func TestUpdateCycle_Idempotent(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = false // already up-to-date

	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(""), e, coreGroup(checkerFor(t, sv), nil), newAlertState()); err != nil {
		t.Fatalf("updateGroupOnce: %v", err)
	}
	if len(e.applied) != 0 {
		t.Fatalf("up-to-date must be a no-op; applied=%v", e.applied)
	}
}

func TestUpdateCycle_DryRun(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	cfg := baseCfg()
	cfg.DryRun = true

	if err := updateGroupOnce(context.Background(), cfg, notify.New(""), e, coreGroup(checkerFor(t, sv), nil), newAlertState()); err != nil {
		t.Fatalf("updateGroupOnce: %v", err)
	}
	if len(e.applied) != 0 {
		t.Fatalf("dry-run must not call Apply; applied=%v", e.applied)
	}
}

func TestUpdateCycle_PriorityOrder(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()

	e := newFakeEngine()
	// deliberately list validator-app first; the runner must reorder participant → validator
	e.workloads = []rollout.Workload{
		{Name: "validator-1", Component: "validator-app"},
		{Name: "participant-1", Component: "canton-participant"},
	}
	e.needsUpdate["validator-app"] = true
	e.needsUpdate["canton-participant"] = true

	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(""), e, coreGroup(checkerFor(t, sv), nil), newAlertState()); err != nil {
		t.Fatalf("updateGroupOnce: %v", err)
	}
	want := []string{"canton-participant", "validator-app"}
	if len(e.applied) != 2 || e.applied[0] != want[0] || e.applied[1] != want[1] {
		t.Fatalf("apply order = %v, want %v", e.applied, want)
	}
}

func TestUpdateCycle_NoConsensus(t *testing.T) {
	// Two SVs disagree → no version reaches 2/3 → no consensus.
	sv1 := svServer(t, "0.8.3")
	defer sv1.Close()
	sv2 := svServer(t, "0.9.0")
	defer sv2.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	checker := release.New(release.Config{
		SVInfoURLs: []string{sv1.URL, sv2.URL},
		Threshold:  2.0 / 3.0,
		Components: []string{"canton-participant", "validator-app"},
	})

	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(""), e, coreGroup(checker, nil), newAlertState()); err != nil {
		t.Fatalf("must return nil on no-consensus: %v", err)
	}
	if len(e.applied) != 0 {
		t.Fatalf("no-consensus must not call Apply; applied=%v", e.applied)
	}
}

func TestUpdateCycle_CoreFailureStops(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{
		{Name: "participant-1", Component: "canton-participant"},
		{Name: "validator-1", Component: "validator-app"},
	}
	e.needsUpdate["canton-participant"] = true
	e.needsUpdate["validator-app"] = true
	e.applyErr["canton-participant"] = errors.New("simulated pull failure")

	err := updateGroupOnce(context.Background(), baseCfg(), notify.New(""), e, coreGroup(checkerFor(t, sv), nil), newAlertState())
	if err == nil {
		t.Fatal("core failure must return an error")
	}
	// validator-app must NOT have been applied after participant failure.
	for _, a := range e.applied {
		if a == "validator-app" {
			t.Fatal("validator-app must not be applied after canton-participant failure")
		}
	}
}

func TestUpdateCycle_OutsideWindowDefers(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	// A one-hour window starting two hours from now is closed right now.
	now := time.Now().UTC()
	s, en := now.Add(2*time.Hour), now.Add(3*time.Hour)
	spec := fmt.Sprintf("%02d:%02d-%02d:%02d", s.Hour(), s.Minute(), en.Hour(), en.Minute())
	w, err := window.Parse(spec, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseCfg()
	cfg.Window = w

	if err := updateGroupOnce(context.Background(), cfg, notify.New(""), e, coreGroup(checkerFor(t, sv), nil), newAlertState()); err != nil {
		t.Fatalf("updateGroupOnce: %v", err)
	}
	if len(e.applied) != 0 {
		t.Fatalf("outside rollout window must defer (no Apply); applied=%v", e.applied)
	}
}

func TestUpdateCycle_ClassifierAlert(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	g := coreGroup(checkerFor(t, sv), classify.Fixed(classify.Alert))
	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(""), e, g, newAlertState()); err != nil {
		t.Fatalf("updateGroupOnce: %v", err)
	}
	if len(e.applied) != 0 {
		t.Fatalf("classifier Alert must not Apply; applied=%v", e.applied)
	}
}

func TestUpdateCycle_ClassifierErrorFailsSafe(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	g := coreGroup(checkerFor(t, sv), errClassifier{})
	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(""), e, g, newAlertState()); err != nil {
		t.Fatalf("updateGroupOnce: %v", err)
	}
	if len(e.applied) != 0 {
		t.Fatalf("classifier error must fail safe (no Apply); applied=%v", e.applied)
	}
}

func TestUpdateCycle_NotifiesOnSuccess(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()
	hook, texts := webhookRecorder(t)
	defer hook.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	g := coreGroup(checkerFor(t, sv), nil)
	g.NotesURL = "https://notes.example/splice"

	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(hook.URL), e, g, newAlertState()); err != nil {
		t.Fatalf("updateGroupOnce: %v", err)
	}
	got := texts()
	if len(got) != 1 || !strings.Contains(got[0], "updated to 0.8.3") || !strings.Contains(got[0], g.NotesURL) {
		t.Fatalf("want one success notice with version and notes link, got %v", got)
	}
}

// Alert-only tracks resolve a version but never Apply; they alert with the notes link.
func TestUpdateCycle_AlertOnlyNeverApplies(t *testing.T) {
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tags":["1.11.2","1.11.3"]}`)
	}))
	defer reg.Close()
	hook, texts := webhookRecorder(t)
	defer hook.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "gw-1", Component: "wallet-gateway"}}
	e.needsUpdate["wallet-gateway"] = true

	g := Group{
		Name:       "wallet-gateway",
		Components: map[string]bool{"wallet-gateway": true},
		Resolver:   release.NewRegistryResolver(reg.URL),
		Classifier: classify.Fixed(classify.Alert),
		NotesURL:   "https://notes.example/gateway",
	}
	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(hook.URL), e, g, newAlertState()); err != nil {
		t.Fatalf("updateGroupOnce: %v", err)
	}
	if len(e.applied) != 0 {
		t.Fatalf("alert-only track must not Apply; applied=%v", e.applied)
	}
	got := texts()
	if len(got) != 1 || !strings.Contains(got[0], "attention") || !strings.Contains(got[0], g.NotesURL) {
		t.Fatalf("want one manual-review alert with notes link, got %v", got)
	}
}

// One tick drives both the consensus core group and a registry-resolved utilities
// group, each to its own version.
func TestTick_MultiGroup(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()
	// utilities registry: latest tag 0.14.5
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tags":["0.14.0","0.14.5","0.14.4"]}`)
	}))
	defer reg.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{
		{Name: "participant-1", Component: "canton-participant"},
		{Name: "frontend-1", Component: "frontend"},
	}
	e.needsUpdate["canton-participant"] = true
	e.needsUpdate["frontend"] = true

	cfg := baseCfg()
	cfg.Groups = []Group{
		coreGroup(checkerFor(t, sv), nil),
		{
			Name:       "utilities",
			Components: map[string]bool{"frontend": true, "utilities-darsyncer-client": true},
			Resolver:   release.NewRegistryResolver(reg.URL),
			Classifier: classify.Fixed(classify.Auto),
		},
	}

	if err := tick(context.Background(), cfg, notify.New(""), e, newAlertState()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(e.applied) != 2 {
		t.Fatalf("expected both groups applied, got %v", e.applied)
	}
	// core is processed before utilities.
	if e.applied[0] != "canton-participant" || e.applied[1] != "frontend" {
		t.Fatalf("apply order = %v, want [canton-participant frontend]", e.applied)
	}
}

// --- Alert dedup + 48h auto-approve ---

func TestAlertDedup_SendsOnFirstCall(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()
	hook, texts := webhookRecorder(t)
	defer hook.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	g := coreGroup(checkerFor(t, sv), classify.Fixed(classify.Alert))
	state := newAlertState()

	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(hook.URL), e, g, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := texts(); len(got) != 1 {
		t.Fatalf("expected 1 alert on first call, got %d", len(got))
	}
}

func TestAlertDedup_SuppressesWithin30min(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()
	hook, texts := webhookRecorder(t)
	defer hook.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	g := coreGroup(checkerFor(t, sv), classify.Fixed(classify.Alert))
	state := newAlertState()
	base := time.Now()
	state.now = func() time.Time { return base }

	// First call at t=0: sends.
	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(hook.URL), e, g, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Second call at t=10min: suppressed (< 30min).
	state.now = func() time.Time { return base.Add(10 * time.Minute) }
	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(hook.URL), e, g, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := texts(); len(got) != 1 {
		t.Fatalf("expected 1 alert (second suppressed), got %d: %v", len(got), got)
	}
}

func TestAlertDedup_ResendsAfter30min(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()
	hook, texts := webhookRecorder(t)
	defer hook.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	g := coreGroup(checkerFor(t, sv), classify.Fixed(classify.Alert))
	state := newAlertState()
	base := time.Now()
	state.now = func() time.Time { return base }

	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(hook.URL), e, g, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 31 minutes later: dedup window expired, resend.
	state.now = func() time.Time { return base.Add(31 * time.Minute) }
	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(hook.URL), e, g, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := texts(); len(got) != 2 {
		t.Fatalf("expected 2 alerts (resend after 30min), got %d: %v", len(got), got)
	}
}

func TestAutoApprove_AfterTimeout_ForceApplies(t *testing.T) {
	sv := svServer(t, "0.8.3")
	defer sv.Close()

	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	g := coreGroup(checkerFor(t, sv), classify.Fixed(classify.Alert))
	state := newAlertState()
	base := time.Now()
	state.now = func() time.Time { return base }

	// First call at t=0: alert sent, no apply.
	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(""), e, g, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(e.applied) != 0 {
		t.Fatal("should not apply before 48h window expires")
	}

	// 49 hours later: auto-approve kicks in, apply proceeds.
	state.now = func() time.Time { return base.Add(49 * time.Hour) }
	if err := updateGroupOnce(context.Background(), baseCfg(), notify.New(""), e, g, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(e.applied) != 1 || e.applied[0] != "canton-participant" {
		t.Fatalf("expected auto-apply after 48h, got %v", e.applied)
	}
}

// --- ApplyForced ---

// fakeVersionResolver returns a fixed version (and optionally Skip).
type fakeVersionResolver struct {
	version string
	skip    bool
}

func (r fakeVersionResolver) Resolve(context.Context) (release.Resolution, error) {
	if r.skip {
		return release.Resolution{Version: r.version, Skip: true, Reason: "no consensus"}, nil
	}
	return release.Resolution{Version: r.version}, nil
}

func TestApplyForced_AppliesMatchingVersion(t *testing.T) {
	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	cfg := baseCfg()
	cfg.Groups = []Group{{
		Name:       "core",
		Components: map[string]bool{"canton-participant": true},
		Resolver:   fakeVersionResolver{version: "0.9.0"},
		Classifier: classify.Fixed(classify.Alert), // classifier bypassed by ApplyForced
	}}

	if err := ApplyForced(context.Background(), cfg, e, "0.9.0"); err != nil {
		t.Fatalf("ApplyForced: %v", err)
	}
	if len(e.applied) != 1 || e.applied[0] != "canton-participant" {
		t.Fatalf("expected canton-participant applied, got %v", e.applied)
	}
}

func TestApplyForced_SkipsWrongVersion(t *testing.T) {
	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	cfg := baseCfg()
	cfg.Groups = []Group{{
		Name:       "core",
		Components: map[string]bool{"canton-participant": true},
		Resolver:   fakeVersionResolver{version: "0.8.3"}, // current target differs
		Classifier: classify.Fixed(classify.Auto),
	}}

	if err := ApplyForced(context.Background(), cfg, e, "0.9.0"); err != nil {
		t.Fatalf("ApplyForced: %v", err)
	}
	if len(e.applied) != 0 {
		t.Fatalf("wrong-version group must not apply; applied=%v", e.applied)
	}
}

func TestApplyForced_BypassesWindow(t *testing.T) {
	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	// A rollout window that is currently closed.
	now := time.Now().UTC()
	s, en := now.Add(2*time.Hour), now.Add(3*time.Hour)
	spec := fmt.Sprintf("%02d:%02d-%02d:%02d", s.Hour(), s.Minute(), en.Hour(), en.Minute())
	w, err := window.Parse(spec, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseCfg()
	cfg.Window = w
	cfg.Groups = []Group{{
		Name:       "core",
		Components: map[string]bool{"canton-participant": true},
		Resolver:   fakeVersionResolver{version: "0.9.0"},
		Classifier: classify.Fixed(classify.Auto),
	}}

	if err := ApplyForced(context.Background(), cfg, e, "0.9.0"); err != nil {
		t.Fatalf("ApplyForced: %v", err)
	}
	if len(e.applied) != 1 {
		t.Fatalf("ApplyForced must ignore rollout window; applied=%v", e.applied)
	}
}

func TestApplyForced_AppliesWithNoConsensus(t *testing.T) {
	e := newFakeEngine()
	e.workloads = []rollout.Workload{{Name: "participant-1", Component: "canton-participant"}}
	e.needsUpdate["canton-participant"] = true

	cfg := baseCfg()
	cfg.Groups = []Group{{
		Name:       "core",
		Components: map[string]bool{"canton-participant": true},
		Resolver:   fakeVersionResolver{version: "0.9.0", skip: true}, // Skip=true, like no consensus
		Classifier: classify.Fixed(classify.Auto),
	}}

	if err := ApplyForced(context.Background(), cfg, e, "0.9.0"); err != nil {
		t.Fatalf("ApplyForced: %v", err)
	}
	if len(e.applied) != 1 {
		t.Fatalf("ApplyForced must apply even without consensus; applied=%v", e.applied)
	}
}

