package tui

import (
	"context"
	"sync"

	tea "github.com/keakon/bubbletea/v2"
)

type historySearchTask struct {
	priority int
	run      func(context.Context) tea.Msg
}

// A single shared worker owns expensive history work, even when Model is copied.
type historySearchRuntime struct {
	mu      sync.Mutex
	pending [3]*historySearchTask
	cancel  context.CancelFunc
	wake    chan struct{}
	results chan tea.Msg
	ctx     context.Context
	stop    context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

func newHistorySearchRuntime() *historySearchRuntime {
	ctx, stop := context.WithCancel(context.Background())
	return &historySearchRuntime{wake: make(chan struct{}, 1), results: make(chan tea.Msg, 1), ctx: ctx, stop: stop, done: make(chan struct{})}
}
func (r *historySearchRuntime) start() { r.once.Do(func() { go r.run() }) }
func (r *historySearchRuntime) submit(task historySearchTask) {
	r.start()
	r.mu.Lock()
	if task.priority == 0 {
		if r.cancel != nil {
			r.cancel()
		}
		r.pending = [3]*historySearchTask{}
	}
	r.pending[task.priority] = &task
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *historySearchRuntime) invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.pending = [3]*historySearchTask{}
	r.mu.Unlock()
}
func (r *historySearchRuntime) close() {
	if r == nil {
		return
	}
	r.start()
	r.stop()
	r.invalidate()
	<-r.done
}
func (r *historySearchRuntime) run() {
	defer close(r.done)
	for {
		if r.ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		var task *historySearchTask
		for i, p := range r.pending {
			if p != nil {
				task = p
				r.pending[i] = nil
				break
			}
		}
		ctx, cancel := context.WithCancel(r.ctx)
		r.cancel = cancel
		r.mu.Unlock()
		if task == nil {
			cancel()
			select {
			case <-r.wake:
			case <-r.ctx.Done():
				return
			}
			continue
		}
		msg := task.run(ctx)
		if ctx.Err() == nil && msg != nil {
			select {
			case r.results <- msg:
			case <-ctx.Done():
			}
		}
		cancel()
	}
}
func (m *Model) waitInputHistorySearch() tea.Cmd {
	r := m.inputHistoryRuntime
	if r == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case msg := <-r.results:
			return msg
		case <-r.ctx.Done():
			return nil
		}
	}
}
