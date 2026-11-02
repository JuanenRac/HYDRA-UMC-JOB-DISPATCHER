package dispatcher

import (
	"errors"
	"math"
	"testing"
	"time"
)

func newBoard(t *testing.T, cfg MaintenanceConfig) (*MaintenanceBoard, *Engine, *time.Time) {
	t.Helper()
	engine := NewEngine()
	b := NewMaintenanceBoard(engine)
	clock := time.Unix(1_000_000, 0)
	b.now = func() time.Time { return clock }
	b.SetConfig(cfg)
	return b, engine, &clock
}

func onConfig() MaintenanceConfig {
	cfg := DefaultMaintenanceConfig()
	cfg.Enabled = true
	return cfg
}

func TestMaintenanceIsOffByDefault(t *testing.T) {
	b, _, _ := newBoard(t, MaintenanceConfig{})
	if b.Enabled() {
		t.Fatal("a board without configuration must be off")
	}
	if _, _, err := b.Propose(ProposalInput{Source: "motor-1", Reason: "drift", Score: 2, Drift: 1}); !errors.Is(err, ErrMaintenanceOff) {
		t.Fatalf("want ErrMaintenanceOff, got %v", err)
	}
	if _, _, err := b.Approve("x", "me"); !errors.Is(err, ErrMaintenanceOff) {
		t.Fatalf("want ErrMaintenanceOff on approve, got %v", err)
	}
}

func TestProposalDoesNotReachTheQueue(t *testing.T) {
	b, engine, _ := newBoard(t, onConfig())
	p, created, err := b.Propose(ProposalInput{Source: "motor-1", Reason: "sustained drift", Score: 3.5, Drift: 1.2})
	if err != nil || !created || p.Status != ProposalProposed {
		t.Fatalf("propose: %+v %v %v", p, created, err)
	}
	if jobs := engine.Jobs(); len(jobs) != 0 {
		t.Fatalf("a proposal must not create a job, found %d", len(jobs))
	}
}

func TestSameSourceUpdatesTheOpenProposal(t *testing.T) {
	b, _, _ := newBoard(t, onConfig())
	first, _, _ := b.Propose(ProposalInput{Source: "motor-1", Reason: "a", Score: 1, Drift: 1})
	again, created, err := b.Propose(ProposalInput{Source: "motor-1", Reason: "b", Score: 5, Drift: 2})
	if err != nil || created || again.ID != first.ID || again.Score != 5 || again.Reason != "b" {
		t.Fatalf("second proposal: %+v %v %v", again, created, err)
	}
	if n := len(b.List("")); n != 1 {
		t.Fatalf("want 1 proposal, got %d", n)
	}
}

func TestApproveSubmitsOneOrdinaryJob(t *testing.T) {
	cfg := onConfig()
	cfg.Priority, cfg.RequiredTool, cfg.Location = 7, "PnP", "cell-A"
	b, engine, _ := newBoard(t, cfg)
	p, _, _ := b.Propose(ProposalInput{Source: "motor-1", Reason: "drift", Score: 3, Drift: 1})
	done, job, err := b.Approve(p.ID, "ana")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != ProposalApproved || done.JobID != job.ID || done.DecidedBy != "ana" {
		t.Fatalf("approved proposal: %+v", done)
	}
	if job.Priority != 7 || job.RequiredTool != "PnP" || job.Location != "cell-A" {
		t.Fatalf("job does not follow the configuration: %+v", job)
	}
	if len(engine.Jobs()) != 1 {
		t.Fatal("approving must submit exactly one job")
	}
	if _, _, err := b.Approve(p.ID, "ana"); !errors.Is(err, ErrProposalDecided) {
		t.Fatalf("a decided proposal cannot be approved again: %v", err)
	}
}

func TestRejectClosesWithoutAJob(t *testing.T) {
	b, engine, _ := newBoard(t, onConfig())
	p, _, _ := b.Propose(ProposalInput{Source: "motor-1", Reason: "drift", Score: 3, Drift: 1})
	done, err := b.Reject(p.ID, "ana", "false alarm")
	if err != nil || done.Status != ProposalRejected || done.DecisionReason != "false alarm" {
		t.Fatalf("reject: %+v %v", done, err)
	}
	if len(engine.Jobs()) != 0 {
		t.Fatal("rejecting must not create a job")
	}
	if _, err := b.Reject("nope", "ana", ""); !errors.Is(err, ErrUnknownProposal) {
		t.Fatalf("want ErrUnknownProposal, got %v", err)
	}
}

func TestCooldownAfterADecisionAndExpiry(t *testing.T) {
	cfg := onConfig()
	cfg.Cooldown, cfg.ExpireAfter = time.Hour, 2*time.Hour
	b, _, clock := newBoard(t, cfg)
	p, _, _ := b.Propose(ProposalInput{Source: "motor-1", Reason: "drift", Score: 3, Drift: 1})
	_, _ = b.Reject(p.ID, "ana", "")
	if _, _, err := b.Propose(ProposalInput{Source: "motor-1", Reason: "drift", Score: 3, Drift: 1}); !errors.Is(err, ErrProposalDecided) {
		t.Fatalf("inside the cooldown a new proposal is refused: %v", err)
	}
	*clock = clock.Add(2 * time.Hour)
	q, created, err := b.Propose(ProposalInput{Source: "motor-1", Reason: "drift", Score: 3, Drift: 1})
	if err != nil || !created {
		t.Fatalf("after the cooldown: %+v %v %v", q, created, err)
	}
	*clock = clock.Add(3 * time.Hour)
	if open := b.List(ProposalProposed); len(open) != 0 {
		t.Fatalf("an open proposal nobody decided must expire, found %d", len(open))
	}
	if exp := b.List(ProposalExpired); len(exp) != 1 {
		t.Fatalf("want 1 expired, got %d", len(exp))
	}
}

func TestProposalInputIsChecked(t *testing.T) {
	b, _, _ := newBoard(t, onConfig())
	bad := []ProposalInput{
		{Source: "", Reason: "x", Score: 1},
		{Source: "has space", Reason: "x"},
		{Source: "ok", Reason: ""},
		{Source: "ok", Reason: "line\nbreak"},
		{Source: "ok", Reason: "x", Score: nan()},
	}
	for _, in := range bad {
		if _, _, err := b.Propose(in); !errors.Is(err, ErrInvalidProposal) {
			t.Errorf("%+v should be invalid, got %v", in, err)
		}
	}
}

func TestMaxOpenLimitsTheList(t *testing.T) {
	cfg := onConfig()
	cfg.MaxOpen = 2
	b, _, _ := newBoard(t, cfg)
	_, _, _ = b.Propose(ProposalInput{Source: "a", Reason: "x"})
	_, _, _ = b.Propose(ProposalInput{Source: "b", Reason: "x"})
	if _, _, err := b.Propose(ProposalInput{Source: "c", Reason: "x"}); !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("the third open proposal must be refused: %v", err)
	}
}

func TestMaintenanceConfigSection(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"maintenance":{"enabled":true,"priority":3,"required_tool":"Laser","cooldown_seconds":60}}`))
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.MaintenanceSetting()
	if !m.Enabled || m.Priority != 3 || m.RequiredTool != "Laser" || m.Cooldown != time.Minute || m.MaxOpen != 100 {
		t.Fatalf("unexpected setting: %+v", m)
	}
	if off, _ := ParseConfig([]byte(`{}`)); off.MaintenanceSetting().Enabled {
		t.Fatal("no section must mean off")
	}
	if off, _ := ParseConfig([]byte(`{"maintenance":{"priority":3}}`)); off.MaintenanceSetting().Enabled {
		t.Fatal("a section without enabled must stay off")
	}
	for _, bad := range []string{`{"maintenance":{"priority":-1}}`, `{"maintenance":{"max_open":0}}`, `{"maintenance":{"cooldown_seconds":-5}}`, `{"maintenance":{"unknown":1}}`} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
}

func nan() float64 { return math.NaN() }
