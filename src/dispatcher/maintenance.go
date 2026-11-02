// HYDRA-UMC-JOB-DISPATCHER - maintenance proposals
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A sustained drift reported by the anomaly detector can ask for a maintenance order, but the order is only a PROPOSAL: it sits in a
// list of its own, never reaches the queue and is never given to a robot until a person approves it. Approving it submits an ordinary
// job (the queue, the tool and the location rules are the usual ones); rejecting it closes it. Nothing here stops a machine.
//
// Off unless the configuration file turns the "maintenance" section on. The proposals are kept in memory only (a restart clears them;
// the detector proposes again while the drift lasts, and a job that was approved is a normal job and persists like any other).
package dispatcher

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Proposal states.
const (
	ProposalProposed = "proposed"
	ProposalApproved = "approved"
	ProposalRejected = "rejected"
	ProposalExpired  = "expired"
)

var (
	// ErrMaintenanceOff is returned while the "maintenance" section is not enabled.
	ErrMaintenanceOff = errors.New("maintenance proposals are switched off")
	// ErrInvalidProposal marks a proposal that cannot be accepted as sent.
	ErrInvalidProposal = errors.New("invalid maintenance proposal")
	// ErrUnknownProposal is returned for an id that is not on the list.
	ErrUnknownProposal = errors.New("unknown maintenance proposal")
	// ErrProposalDecided is returned when a proposal is no longer open.
	ErrProposalDecided = errors.New("the maintenance proposal is not open any more")
)

var proposalSafeText = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// MaintenanceConfig is the tuned behaviour of the proposals (the "maintenance" section of the configuration file).
type MaintenanceConfig struct {
	Enabled bool
	// Priority, RequiredTool and Location are those of the job that approving a proposal submits.
	Priority     int
	RequiredTool string
	Location     string
	// MaxOpen is how many proposals may wait for a decision at once (a further one is refused).
	MaxOpen int
	// Cooldown: after a proposal for a source was decided, another for the same source is not accepted for this long.
	Cooldown time.Duration
	// ExpireAfter: an open proposal nobody decided is closed after this long.
	ExpireAfter time.Duration
}

// DefaultMaintenanceConfig holds the values used for a field the file leaves out.
func DefaultMaintenanceConfig() MaintenanceConfig {
	return MaintenanceConfig{Priority: 1, MaxOpen: 100, Cooldown: 24 * time.Hour, ExpireAfter: 7 * 24 * time.Hour}
}

// Proposal is one maintenance order waiting for, or already given, a person's decision.
type Proposal struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Reason string `json:"reason"`
	// Score is the latest anomaly score and Drift the drift score that raised it (both as the detector measured them).
	Score          float64 `json:"score"`
	Drift          float64 `json:"drift"`
	Status         string  `json:"status"`
	JobID          string  `json:"jobId,omitempty"`
	CreatedAtMs    int64   `json:"createdAtMs"`
	UpdatedAtMs    int64   `json:"updatedAtMs"`
	DecidedAtMs    int64   `json:"decidedAtMs,omitempty"`
	DecidedBy      string  `json:"decidedBy,omitempty"`
	DecisionReason string  `json:"decisionReason,omitempty"`
}

// ProposalInput is what a proposer (the anomaly detector) sends.
type ProposalInput struct {
	Source string
	Reason string
	Score  float64
	Drift  float64
}

// MaintenanceBoard is the list of proposals.
type MaintenanceBoard struct {
	mu      sync.Mutex
	engine  *Engine
	cfg     MaintenanceConfig
	items   []*Proposal
	lastEnd map[string]time.Time // source -> when its last proposal was decided or expired
	seq     int
	now     func() time.Time
}

// NewMaintenanceBoard builds a board that submits approved proposals to engine. It is off until SetConfig enables it.
func NewMaintenanceBoard(engine *Engine) *MaintenanceBoard {
	return &MaintenanceBoard{engine: engine, lastEnd: map[string]time.Time{}, now: time.Now}
}

// SetConfig installs the behaviour (callable while the service runs). Proposals already on the list stay.
func (b *MaintenanceBoard) SetConfig(cfg MaintenanceConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfg = cfg
}

// Enabled reports whether the proposals are switched on.
func (b *MaintenanceBoard) Enabled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg.Enabled
}

func (b *MaintenanceBoard) expireLocked(now time.Time) {
	if b.cfg.ExpireAfter <= 0 {
		return
	}
	for _, p := range b.items {
		if p.Status == ProposalProposed && now.Sub(time.UnixMilli(p.CreatedAtMs)) > b.cfg.ExpireAfter {
			p.Status = ProposalExpired
			p.UpdatedAtMs = now.UnixMilli()
			p.DecidedAtMs = p.UpdatedAtMs
			b.lastEnd[p.Source] = now
		}
	}
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Propose adds a proposal. created is false when an open proposal for the same source already exists (its latest evidence is updated
// and it is returned). A source decided less than Cooldown ago is refused with ErrProposalDecided.
func (b *MaintenanceBoard) Propose(in ProposalInput) (Proposal, bool, error) {
	if !proposalSafeText.MatchString(in.Source) {
		return Proposal{}, false, fmt.Errorf("%w: \"source\" must be 1 to 128 characters of letters, digits, \"_\", \"-\", \".\" or \":\"", ErrInvalidProposal)
	}
	if len(in.Reason) == 0 || len(in.Reason) > 500 {
		return Proposal{}, false, fmt.Errorf("%w: \"reason\" must be 1 to 500 characters", ErrInvalidProposal)
	}
	for _, r := range in.Reason {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return Proposal{}, false, fmt.Errorf("%w: \"reason\" must not contain control characters", ErrInvalidProposal)
		}
	}
	if !finite(in.Score) || !finite(in.Drift) {
		return Proposal{}, false, fmt.Errorf("%w: \"score\" and \"drift\" must be finite numbers", ErrInvalidProposal)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.cfg.Enabled {
		return Proposal{}, false, ErrMaintenanceOff
	}
	now := b.now()
	b.expireLocked(now)
	open := 0
	for _, p := range b.items {
		if p.Status != ProposalProposed {
			continue
		}
		open++
		if p.Source == in.Source {
			p.Score, p.Drift, p.Reason, p.UpdatedAtMs = in.Score, in.Drift, in.Reason, now.UnixMilli()
			return *p, false, nil
		}
	}
	if ended, ok := b.lastEnd[in.Source]; ok && b.cfg.Cooldown > 0 && now.Sub(ended) < b.cfg.Cooldown {
		return Proposal{}, false, fmt.Errorf("%w: a proposal for this source was decided less than %s ago", ErrProposalDecided, b.cfg.Cooldown)
	}
	if b.cfg.MaxOpen > 0 && open >= b.cfg.MaxOpen {
		return Proposal{}, false, fmt.Errorf("%w: %d proposals already wait for a decision", ErrInvalidProposal, open)
	}
	b.seq++
	p := &Proposal{
		ID: fmt.Sprintf("mp-%d-%d", now.UnixMilli(), b.seq), Source: in.Source, Reason: in.Reason, Score: in.Score, Drift: in.Drift,
		Status: ProposalProposed, CreatedAtMs: now.UnixMilli(), UpdatedAtMs: now.UnixMilli(),
	}
	b.items = append(b.items, p)
	return *p, true, nil
}

// List returns the proposals, newest first; status "" returns all of them.
func (b *MaintenanceBoard) List(status string) []Proposal {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked(b.now())
	out := make([]Proposal, 0, len(b.items))
	for _, p := range b.items {
		if status == "" || p.Status == status {
			out = append(out, *p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAtMs > out[j].CreatedAtMs })
	return out
}

func (b *MaintenanceBoard) openLocked(id string) (*Proposal, error) {
	for _, p := range b.items {
		if p.ID == id {
			if p.Status != ProposalProposed {
				return nil, ErrProposalDecided
			}
			return p, nil
		}
	}
	return nil, ErrUnknownProposal
}

// Approve is a person's decision: it submits the job of the proposal (the usual queue rules apply) and closes the proposal.
func (b *MaintenanceBoard) Approve(id, by string) (Proposal, Job, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.cfg.Enabled {
		return Proposal{}, Job{}, ErrMaintenanceOff
	}
	now := b.now()
	b.expireLocked(now)
	p, err := b.openLocked(id)
	if err != nil {
		return Proposal{}, Job{}, err
	}
	job, _, err := b.engine.SubmitJob(Job{
		ID: "maint-" + p.ID, Priority: b.cfg.Priority, RequiredTool: b.cfg.RequiredTool, Location: b.cfg.Location, DedupKey: "maintenance:" + p.ID,
	})
	if err != nil {
		return Proposal{}, Job{}, err
	}
	p.Status, p.JobID = ProposalApproved, job.ID
	p.UpdatedAtMs, p.DecidedAtMs, p.DecidedBy = now.UnixMilli(), now.UnixMilli(), by
	b.lastEnd[p.Source] = now
	return *p, job, nil
}

// Reject is a person's decision to close the proposal without a job.
func (b *MaintenanceBoard) Reject(id, by, reason string) (Proposal, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.cfg.Enabled {
		return Proposal{}, ErrMaintenanceOff
	}
	now := b.now()
	b.expireLocked(now)
	p, err := b.openLocked(id)
	if err != nil {
		return Proposal{}, err
	}
	p.Status = ProposalRejected
	p.UpdatedAtMs, p.DecidedAtMs, p.DecidedBy, p.DecisionReason = now.UnixMilli(), now.UnixMilli(), by, reason
	b.lastEnd[p.Source] = now
	return *p, nil
}
