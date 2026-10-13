package inputhistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

type SaveState string

const (
	Pending   SaveState = "pending"
	Failed    SaveState = "failed"
	Unknown   SaveState = "unknown"
	Synced    SaveState = "synced"
	Cancelled SaveState = "cancelled"
)

type AdmissionOutcome string

const (
	Queued    AdmissionOutcome = "queued"
	LocalOnly AdmissionOutcome = "local_only"
	Disabled  AdmissionOutcome = "disabled"
	TooLarge  AdmissionOutcome = "too_large"
	Closed    AdmissionOutcome = "closed"
)

type Entry struct {
	Record Record
	State  SaveState
	Order  uint64
}
type Snapshot struct {
	Version uint64
	Entries []Entry
	cache   *searchCache
}
type Admission struct {
	ID       string
	Sequence uint64
	Outcome  AdmissionOutcome
	Retained bool
	Err      error
}
type ClearResult struct {
	Outcome SaveState
	Err     error
}
type ClearTicket struct {
	Sequence uint64
	Cutoff   uint64
	Result   <-chan ClearResult
}
type Status struct {
	Version             uint64
	Reason              AdmissionOutcome
	State               SaveState
	ID                  string
	Count               uint64
	Unconfirmed         int
	Closing             bool
	Recording, Retained bool
	Err                 error
}
type CloseError struct {
	Unconfirmed int
	Err         error
}

func (e *CloseError) Error() string { return "input history close: " + e.Err.Error() }
func (e *CloseError) Unwrap() error { return e.Err }

type localEntry struct {
	entry    Entry
	sequence uint64
	size     int64
}
type operation struct {
	sequence uint64
	record   *Record
	clear    *clearCompletion
}
type clearCompletion struct {
	once   sync.Once
	result chan ClearResult
}

func (c *clearCompletion) finish(result ClearResult) {
	c.once.Do(func() { c.result <- result; close(c.result) })
}

type refreshRequest struct {
	ctx    context.Context
	result chan error
}
type Store struct {
	mu                sync.Mutex
	hooks             fileHooks
	opts              Options
	path, pathLock    string
	sequence, version uint64
	local             []localEntry
	localBytes        int64
	diagnosticInfo    os.FileInfo
	disk              []Record
	queue             []operation
	inflight          []operation
	queueBytes        int64
	clearing          bool
	closing           bool
	status            Status
	wake, notify      chan struct{}
	refresh           chan refreshRequest
	done              chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	searchCache       *searchCache
	cache             diskView // writer-owned, always validated while holding the sidecar lock
}

func New(stateDir string, opts Options) *Store { return newStore(stateDir, opts, fileHooks{}) }
func newStore(stateDir string, opts Options, hooks fileHooks) *Store {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Store{hooks: hooks, opts: opts, path: filepath.Join(stateDir, "input-history.jsonl"), pathLock: filepath.Join(stateDir, "input-history.lock"), wake: make(chan struct{}, 1), notify: make(chan struct{}, 1), refresh: make(chan refreshRequest, 1), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	go s.run()
	return s
}
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (s *Store) changed() { s.version++; s.searchCache = nil; signal(s.notify) }
func (s *Store) issue(id string, out AdmissionOutcome, state SaveState, err error) {
	s.status.ID = id
	s.status.Reason = out
	s.status.State = state
	s.status.Err = err
	s.status.Count++
	s.changed()
}
func (s *Store) TryRecord(r Record) Admission {
	a := Admission{ID: r.ID}
	if !s.opts.Enabled {
		a.Outcome = Disabled
		return a
	}
	if err := s.opts.Validate(); err != nil {
		a.Outcome = LocalOnly
		a.Err = err
		return a
	}
	if err := r.Validate(); err != nil {
		a.Outcome = LocalOnly
		if errors.Is(err, ErrTooLarge) {
			a.Outcome = TooLarge
		}
		a.Err = err
		return a
	}
	var raw int64
	for _, p := range r.TextParts {
		raw += int64(len(p.Text))
	}
	if raw > min(int64(MaxRecordBytes), s.opts.MaxBytes) || len(r.TextParts) > MaxParts {
		a.Outcome = TooLarge
		a.Err = ErrTooLarge
		return a
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		a.Outcome = Closed
		return a
	}
	r.TextParts = append([]TextPart(nil), r.TextParts...)
	s.sequence++
	a.Sequence = s.sequence
	a.Outcome = Queued
	state := Pending
	if len(s.queue) >= 128 || s.queueBytes+r.Size() > min(int64(8<<20), s.opts.MaxBytes) {
		a.Outcome = LocalOnly
		state = Failed
		a.Err = errors.New("input history queue full")
	} else {
		s.queue = append(s.queue, operation{sequence: s.sequence, record: &r})
		s.queueBytes += r.Size()
		signal(s.wake)
	}
	recordBytes := r.Size()
	s.local = append(s.local, localEntry{Entry{Record: r, State: state}, s.sequence, recordBytes})
	s.localBytes += recordBytes
	cut := 0
	for len(s.local)-cut > s.opts.MaxEntries || s.localBytes > s.opts.MaxBytes {
		s.localBytes -= s.local[cut].size
		s.local[cut] = localEntry{}
		cut++
	}
	s.local = s.local[cut:]
	a.Retained = len(s.local) > 0
	if a.Err != nil {
		s.issue(r.ID, a.Outcome, state, a.Err)
	} else {
		s.changed()
	}
	return a
}
func (s *Store) Notifications() <-chan struct{} { return s.notify }
func (s *Store) Done() <-chan struct{}          { return s.done }
func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.status
	v.Version = s.version
	v.Closing = s.closing
	v.Recording = s.opts.Enabled
	for _, e := range s.local {
		if e.entry.Record.ID == v.ID {
			v.Retained = true
			break
		}
	}
	v.Unconfirmed = len(s.local)
	return v
}
func (s *Store) view() Snapshot {
	s.mu.Lock()
	if s.searchCache == nil {
		s.searchCache = &searchCache{maxBytes: s.opts.MaxBytes}
	}
	v := Snapshot{Version: s.version, cache: s.searchCache}
	disk := s.disk // Published records and their parts are immutable.
	local := append([]localEntry(nil), s.local...)
	s.mu.Unlock()
	for _, r := range disk {
		v.Entries = append(v.Entries, Entry{Record: r, State: Synced})
	}
	for _, e := range local {
		v.Entries = append(v.Entries, e.entry)
	}
	var size int64
	cut := len(v.Entries)
	for cut > 0 {
		r := v.Entries[cut-1].Record
		n := r.Size()
		if len(v.Entries)-cut >= s.opts.MaxEntries || size+n > s.opts.MaxBytes {
			break
		}
		size += n
		cut--
	}
	v.Entries = v.Entries[cut:]
	for i := range v.Entries {
		v.Entries[i].Order = uint64(i + 1)
		v.Entries[i].Record.TextParts = append([]TextPart(nil), v.Entries[i].Record.TextParts...)
	}
	return v
}
func (s *Store) Snapshot(ctx context.Context, refresh bool) (Snapshot, error) {
	if !refresh {
		return s.view(), nil
	}
	req := refreshRequest{ctx, make(chan error, 1)}
	select {
	case s.refresh <- req:
	case <-ctx.Done():
		return s.view(), ctx.Err()
	case <-s.done:
		return s.view(), nil
	}
	select {
	case err := <-req.result:
		return s.view(), err
	case <-ctx.Done():
		return s.view(), ctx.Err()
	case <-s.done:
		return s.view(), nil
	}
}
func (s *Store) TryClear() (ClearTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.clearing {
		return ClearTicket{}, errors.New("input history closed or clear busy")
	}
	cutoff := s.sequence
	s.sequence++
	ch := &clearCompletion{result: make(chan ClearResult, 1)}
	s.clearing = true
	s.queue = append(s.queue, operation{sequence: s.sequence, clear: ch})
	signal(s.wake)
	return ClearTicket{s.sequence, cutoff, ch.result}, nil
}
func (s *Store) publish(d diskView) {
	s.mu.Lock()
	previous := s.disk
	s.mu.Unlock()
	// Disk publication is writer-owned. Compare potentially large text outside
	// the admission mutex; the immutable prior slice remains safe to inspect.
	diskChanged := !reflect.DeepEqual(previous, d.records)
	s.mu.Lock()
	defer s.mu.Unlock()

	local := make([]localEntry, 0, len(s.local))
	for _, e := range s.local {
		if _, ok := d.ids[e.entry.Record.ID]; !ok {
			local = append(local, e)
		} else {
			s.localBytes -= e.size
		}
	}
	if diskChanged || len(local) != len(s.local) {
		s.disk = d.records
		s.local = local
		s.changed()
	}
}
func (s *Store) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	signal(s.wake)
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.cancel()
		s.mu.Lock()
		ids := make(map[string]bool, len(s.local)+len(s.queue)+len(s.inflight))
		for i := range s.local {
			e := &s.local[i]
			ids[e.entry.Record.ID] = true
			if e.entry.State == Pending {
				e.entry.State = Failed
			}
		}
		for _, op := range s.queue {
			if op.record != nil {
				ids[op.record.ID] = true
			}
			if op.clear != nil {
				op.clear.finish(ClearResult{Cancelled, ctx.Err()})
			}
		}
		for _, op := range s.inflight {
			if op.record != nil {
				ids[op.record.ID] = true
			}
			if op.clear != nil {
				op.clear.finish(ClearResult{Unknown, ctx.Err()})
			}
		}
		s.queue = nil
		s.queueBytes = 0
		s.clearing = false
		s.changed()
		s.mu.Unlock()
		return &CloseError{len(ids), ctx.Err()}
	}
}
