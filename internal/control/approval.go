package control

import (
	"context"
	"sort"
	"sync"
	"time"
)

// ApprovalDecision is the outcome the GUI sends back for a pending request.
type ApprovalDecision int

const (
	DecisionPending ApprovalDecision = iota
	DecisionApproved
	DecisionRejected
)

// PendingRequest describes a client connection waiting for operator approval.
type PendingRequest struct {
	ClientName  string    `json:"client_name"`
	Addr        string    `json:"addr"`
	Fingerprint string    `json:"fingerprint"`
	RequestedAt time.Time `json:"requested_at"`

	decision chan ApprovalDecision
}

// Approver is a thread-safe registry of pending TOFU approval requests. A
// dial loop calls Submit which blocks until the operator decides via the
// admin GUI (Decide), the context is cancelled, or the request expires.
type Approver struct {
	mu      sync.RWMutex
	pending map[string]*PendingRequest // keyed by ClientName
}

func NewApprover() *Approver {
	return &Approver{pending: make(map[string]*PendingRequest)}
}

// Submit registers a pending request and blocks until the operator decides
// or ctx is cancelled. Returns the final decision.
//
// Concurrent submissions for the same ClientName replace the previous
// pending request — only the latest one is shown to the operator.
func (a *Approver) Submit(ctx context.Context, name, addr, fingerprint string) ApprovalDecision {
	req := &PendingRequest{
		ClientName:  name,
		Addr:        addr,
		Fingerprint: fingerprint,
		RequestedAt: time.Now(),
		decision:    make(chan ApprovalDecision, 1),
	}

	a.mu.Lock()
	if prev, ok := a.pending[name]; ok {
		// Tell the previous waiter it has been superseded.
		select {
		case prev.decision <- DecisionRejected:
		default:
		}
	}
	a.pending[name] = req
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		// Only delete if we are still the registered request — a new submission
		// might have replaced us.
		if cur, ok := a.pending[name]; ok && cur == req {
			delete(a.pending, name)
		}
		a.mu.Unlock()
	}()

	select {
	case d := <-req.decision:
		return d
	case <-ctx.Done():
		return DecisionRejected
	}
}

// Decide resolves the pending request for name. Returns true if a request was
// found and resolved, false if no such pending request exists.
func (a *Approver) Decide(name string, approve bool) bool {
	a.mu.RLock()
	req, ok := a.pending[name]
	a.mu.RUnlock()
	if !ok {
		return false
	}
	d := DecisionRejected
	if approve {
		d = DecisionApproved
	}
	select {
	case req.decision <- d:
		return true
	default:
		return false
	}
}

// List returns a snapshot of all currently pending requests sorted by name.
func (a *Approver) List() []PendingRequest {
	a.mu.RLock()
	out := make([]PendingRequest, 0, len(a.pending))
	for _, p := range a.pending {
		out = append(out, PendingRequest{
			ClientName:  p.ClientName,
			Addr:        p.Addr,
			Fingerprint: p.Fingerprint,
			RequestedAt: p.RequestedAt,
		})
	}
	a.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ClientName < out[j].ClientName })
	return out
}
