// Package executiongate owns session execution admission without cancelling work.
package executiongate

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Kind string

const (
	LLM        Kind = "llm"
	Tool       Kind = "tool"
	Structural Kind = "structural"
)

type ErrSessionPaused struct{}

func (*ErrSessionPaused) Error() string { return "session transfer paused; execution not started" }

func IsPaused(err error) bool {
	_, ok := errors.AsType[*ErrSessionPaused](err)
	return ok
}

type Activity struct {
	Kind      Kind      `json:"kind"`
	Owner     string    `json:"owner"`
	StartedAt time.Time `json:"started_at"`
}

type Snapshot struct {
	Paused     bool
	FrozenAt   time.Time
	Activities []Activity
}

type FreezeState struct {
	FrozenAt time.Time
	Active   int
}

// Gate belongs to one session. Its zero value accepts execution.
type Gate struct {
	mu       sync.Mutex
	paused   bool
	frozenAt time.Time
	frozen   chan struct{}
	active   map[*Lease]Activity
}

type Lease struct {
	gate *Gate
	once sync.Once
}

func (g *Gate) TryBegin(kind Kind, owner string) (*Lease, error) {
	if g == nil {
		return &Lease{}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		return nil, &ErrSessionPaused{}
	}
	if g.active == nil {
		g.active = make(map[*Lease]Activity)
	}
	l := &Lease{gate: g}
	g.active[l] = Activity{Kind: kind, Owner: owner, StartedAt: time.Now()}
	return l, nil
}

func (l *Lease) Finish() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.gate == nil {
			return
		}
		l.gate.mu.Lock()
		delete(l.gate.active, l)
		l.gate.mu.Unlock()
	})
}

func (g *Gate) Freeze() FreezeState {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused {
		if g.frozen == nil {
			g.frozen = make(chan struct{})
		}
		g.paused = true
		g.frozenAt = time.Now()
		close(g.frozen)
	}
	return FreezeState{FrozenAt: g.frozenAt, Active: len(g.active)}
}

func (g *Gate) Resume() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.active) != 0 {
		return errors.New("session still has active execution")
	}
	g.paused = false
	g.frozenAt = time.Time{}
	g.frozen = make(chan struct{})
	return nil
}

func (g *Gate) Snapshot() Snapshot {
	if g == nil {
		return Snapshot{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := Snapshot{Paused: g.paused, FrozenAt: g.frozenAt}
	for _, activity := range g.active {
		s.Activities = append(s.Activities, activity)
	}
	return s
}

func (g *Gate) Frozen() <-chan struct{} {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.frozen == nil {
		g.frozen = make(chan struct{})
	}
	return g.frozen
}

type contextKey struct{}
type binding struct {
	gate  *Gate
	owner string
}

func WithContext(ctx context.Context, gate *Gate, owner string) context.Context {
	if gate == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, binding{gate, owner})
}

func FromContext(ctx context.Context) *Gate {
	b, _ := ctx.Value(contextKey{}).(binding)
	return b.gate
}

func Begin(ctx context.Context, kind Kind) (*Lease, error) {
	b, _ := ctx.Value(contextKey{}).(binding)
	return b.gate.TryBegin(kind, b.owner)
}

func Check(ctx context.Context) error {
	select {
	case <-FromContext(ctx).Frozen():
		return &ErrSessionPaused{}
	default:
		return nil
	}
}

type leaseContextKey struct{}

// WithLease carries proof of an execution that has already started. It never
// admits an additional request or tool and has no effect after Finish.
func WithLease(ctx context.Context, lease *Lease) context.Context {
	return context.WithValue(ctx, leaseContextKey{}, lease)
}

// HasActiveLease validates runtime membership under the same gate lock used by
// Freeze and Finish. A foreign or finished lease cannot authorize new work.
func HasActiveLease(ctx context.Context, gate *Gate, kind Kind) bool {
	lease, _ := ctx.Value(leaseContextKey{}).(*Lease)
	if gate == nil || lease == nil || lease.gate != gate {
		return false
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	activity, ok := gate.active[lease]
	return ok && activity.Kind == kind
}
