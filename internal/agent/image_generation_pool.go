package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

// Each operation owns its runner; only the successful pool cursor is shared.
type imageGenerationPool struct {
	agent    *MainAgent
	backends []tools.ImageGenerationBackend
	mu       sync.Mutex
	cursor   int
}

func NewImageGenerationPool(a *MainAgent, backends []tools.ImageGenerationBackend) tools.ImageGenerationBackend {
	return &imageGenerationPool{agent: a, backends: slices.Clone(backends)}
}

func (p *imageGenerationPool) Targets() []imagegen.Target {
	targets := make([]imagegen.Target, 0, len(p.backends))
	for _, backend := range p.backends {
		targets = append(targets, backend.Target())
	}
	return targets
}

func (p *imageGenerationPool) Target() imagegen.Target {
	var t imagegen.Target
	union := func(dst, src []string) []string {
		for _, v := range src {
			if !slices.Contains(dst, v) {
				dst = append(dst, v)
			}
		}
		return dst
	}
	for _, b := range p.backends {
		v := b.Target()
		t.Edit = t.Edit || v.Edit
		t.Sizes = union(t.Sizes, v.Sizes)
		t.Ratios = union(t.Ratios, v.Ratios)
		t.Qualities = union(t.Qualities, v.Qualities)
		t.Formats = union(t.Formats, v.Formats)
		t.Backgrounds = union(t.Backgrounds, v.Backgrounds)
	}
	return t
}
func (p *imageGenerationPool) Timeout() time.Duration { return p.backends[0].Timeout() }
func (p *imageGenerationPool) Check(ctx context.Context, r imagegen.Request) error {
	return p.backends[0].Check(ctx, r)
}
func (p *imageGenerationPool) Prepare(ctx context.Context, r imagegen.Request) (tools.ImageGenerationBackend, error) {
	if err := p.Check(ctx, r); err != nil {
		return nil, err
	}
	p.mu.Lock()
	start := p.cursor
	p.mu.Unlock()
	var indices []int
	var skipped []string
	for offset := range len(p.backends) {
		i := (start + offset) % len(p.backends)
		request := r
		target := p.backends[i].Target()
		if err := target.Validate(&request); err == nil {
			indices = append(indices, i)
		} else {
			skipped = append(skipped, target.Provider+"/"+target.Model+": "+err.Error())
		}
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("no image pool target supports the requested operation and parameters: %s", strings.Join(skipped, "; "))
	}
	timeout := p.Timeout()
	var turn *Turn
	id := tools.AgentIDFromContext(ctx)
	if isMainCaller(id, p.agent.instanceID) {
		turn = p.agent.currentTurn()
	} else if sub := p.agent.subAgentByID(id); sub != nil {
		turn = sub.currentTurn()
	}
	if turn != nil && turn.ID == tools.TurnIDFromContext(ctx) && turn.nativeImageFallback.Load() {
		timeout = min(timeout, time.Until(time.Unix(0, turn.nativeImageDeadline.Load())))
		if timeout <= 0 {
			return nil, fmt.Errorf("image fallback time budget expired; do not regenerate")
		}
	}
	return &imagePoolOperation{pool: p, indices: indices, current: indices[0], timeout: timeout, skipped: skipped}, nil
}
func (p *imageGenerationPool) Restore(ctx context.Context, fingerprint string) (tools.ImageGenerationBackend, error) {
	if err := p.Check(ctx, imagegen.Request{}); err != nil {
		return nil, err
	}
	for _, b := range p.backends {
		if tools.ImageTargetFingerprint(b.Target()) == fingerprint {
			return b, nil
		}
	}
	return nil, fmt.Errorf("saved image target is no longer configured; generation must not be replayed")
}
func (*imageGenerationPool) Run(context.Context, imagegen.Request, func() error) (*imagegen.Result, error) {
	return nil, fmt.Errorf("image pool must be prepared before execution")
}
func (*imageGenerationPool) Download(context.Context, string) (imagegen.Image, error) {
	return imagegen.Image{}, fmt.Errorf("image download requires the saved target")
}

type imagePoolOperation struct {
	pool    *imageGenerationPool
	indices []int
	current int
	timeout time.Duration
	skipped []string
}

func (o *imagePoolOperation) Target() imagegen.Target { return o.pool.backends[o.current].Target() }
func (o *imagePoolOperation) Timeout() time.Duration  { return o.timeout }
func (o *imagePoolOperation) Check(ctx context.Context, r imagegen.Request) error {
	return o.pool.Check(ctx, r)
}
func (o *imagePoolOperation) Download(ctx context.Context, url string) (imagegen.Image, error) {
	return o.pool.backends[o.current].Download(ctx, url)
}
func (o *imagePoolOperation) Restore(ctx context.Context, fingerprint string) (tools.ImageGenerationBackend, error) {
	return o.pool.Restore(ctx, fingerprint)
}
func (o *imagePoolOperation) Run(ctx context.Context, r imagegen.Request, before func() error) (*imagegen.Result, error) {
	var last error
	for _, i := range o.indices {
		o.current = i
		if err := o.Check(ctx, r); err != nil {
			return nil, &imagegen.Failure{State: imagegen.StateNotSent, Cause: err}
		}
		result, err := o.pool.backends[i].Run(ctx, r, before)
		if err == nil {
			o.pool.mu.Lock()
			o.pool.cursor = i
			o.pool.mu.Unlock()
			return result, nil
		}
		last = err
		failure, ok := errors.AsType[*imagegen.Failure](err)
		if ok && failure.State == imagegen.StateNotSent && ctx.Err() == nil {
			_, cooling := errors.AsType[*llm.AllKeysCoolingError](err)
			_, exhausted := errors.AsType[*llm.NoUsableKeysError](err)
			if cooling || exhausted {
				continue
			}
		}
		if !ok || failure.State != imagegen.StateRejected || !failure.RetryKey || ctx.Err() != nil {
			return nil, o.failureWithSkippedTargets(err)
		}
	}
	return nil, o.failureWithSkippedTargets(last)
}

func (o *imagePoolOperation) failureWithSkippedTargets(err error) error {
	failure, ok := errors.AsType[*imagegen.Failure](err)
	if !ok || len(o.skipped) == 0 {
		return err
	}
	copy := *failure
	copy.Details.SkippedTargets = slices.Clone(o.skipped)
	return &copy
}
