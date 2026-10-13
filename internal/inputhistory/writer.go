package inputhistory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"time"
)

func (s *Store) run() {
	defer close(s.done)
	defer s.cancel()
	for {
		// Service refresh between batches without changing mutation ordering.
		select {
		case req := <-s.refresh:
			s.refreshDisk(req)
		default:
		}
		s.mu.Lock()
		if len(s.queue) == 0 {
			closing := s.closing
			s.mu.Unlock()
			if closing {
				return
			}
			select {
			case <-s.wake:
			case req := <-s.refresh:
				s.refreshDisk(req)
			case <-s.ctx.Done():
			}
			continue
		}
		op := s.queue[0]
		count := 1
		if op.clear == nil {
			var raw int64
			for count = 0; count < len(s.queue) && count < 32; count++ {
				next := s.queue[count]
				if next.clear != nil || (count > 0 && raw+next.record.Size() > 1<<20) {
					break
				}
				raw += next.record.Size()
			}
		}
		batch := append([]operation(nil), s.queue[:count]...)
		for i := range count {
			if s.queue[i].record != nil {
				s.queueBytes -= s.queue[i].record.Size()
			}
			s.queue[i] = operation{}
		}
		s.queue = s.queue[count:]
		s.inflight = batch
		s.mu.Unlock()
		if op.clear != nil {
			s.clearDisk(op)
		} else {
			s.encodeBatch(batch)
		}
		s.mu.Lock()
		s.inflight = nil
		s.mu.Unlock()
	}
}
func (s *Store) refreshDisk(req refreshRequest) {
	ctx, cancel := context.WithTimeout(req.ctx, 250*time.Millisecond)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	f, err := s.acquire(ctx)
	if err == nil {
		var d diskView
		d, err = s.readDisk()
		if err == nil {
			s.cache = d
			s.publish(d)
		}
		release(f)
	}
	req.result <- err
}
func (s *Store) settle(op operation, state SaveState, err error, tooLarge bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.local {
		if s.local[i].sequence == op.sequence {
			if tooLarge {
				s.localBytes -= s.local[i].size
				s.local = slices.Delete(s.local, i, i+1)
			} else {
				s.local[i].entry.State = state
			}
			break
		}
	}
	if err != nil {
		out := LocalOnly
		if tooLarge {
			out = TooLarge
		}
		s.issue(op.record.ID, out, state, err)
	} else {
		s.changed()
	}
}
func (s *Store) currentDisk() (diskView, error) {
	info, err := os.Stat(s.path)
	old := s.cache.info
	if err == nil && old != nil && os.SameFile(old, info) && old.Size() == info.Size() && old.ModTime() == info.ModTime() {
		return s.cache, nil
	}
	return s.readDisk()
}

// encodeBatch keeps encoded batches at 1 MiB; larger individual records stand alone.
func (s *Store) encodeBatch(ops []operation) {
	var batch []operation
	var data []byte
	var sizes []int64
	flush := func() {
		if len(batch) > 0 {
			s.appendDisk(batch, data, sizes)
		}
		batch = nil
		data = nil
		sizes = nil
	}
	for _, op := range ops {
		if s.ctx.Err() != nil {
			s.settle(op, Failed, s.ctx.Err(), false)
			continue
		}
		line, err := json.Marshal(op.record)
		line = append(line, '\n')
		if err != nil || len(line) > MaxRecordBytes || int64(len(line)) > s.opts.MaxBytes {
			s.settle(op, Failed, ErrTooLarge, true)
			continue
		}
		if len(data)+len(line) > 1<<20 {
			flush()
		}
		batch = append(batch, op)
		data = append(data, line...)
		sizes = append(sizes, int64(len(line)))
	}
	flush()
}
func (s *Store) appendDisk(ops []operation, b []byte, sizes []int64) {
	settle := func(state SaveState, err error) {
		for _, op := range ops {
			s.settle(op, state, err, false)
		}
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	lock, err := s.acquire(ctx)
	if err != nil {
		settle(Failed, err)
		return
	}
	defer release(lock)
	d, err := s.currentDisk()
	if err != nil {
		settle(Failed, err)
		return
	}
	// Appending beyond the published slice length does not mutate any existing
	// snapshot element. Only duplicate removal and trimming need fresh slices.
	if d.ids == nil {
		d.indexIDs()
	}
	for i, op := range ops {
		if old, ok := d.ids[op.record.ID]; ok {
			records := make([]Record, 0, len(d.records))
			records = append(records, d.records[:old]...)
			records = append(records, d.records[old+1:]...)
			d.records = records
			copied := append([]int64(nil), d.sizes[:old]...)
			d.sizes = append(copied, d.sizes[old+1:]...)
			d.repair = true
			d.indexIDs()
		}
		d.ids[op.record.ID] = len(d.records)
		d.records = append(d.records, *op.record)
		d.sizes = append(d.sizes, sizes[i])
	}
	size := int64(0)
	if d.info != nil {
		size = d.info.Size()
	}
	overflow := len(d.records) > s.opts.MaxEntries || size+int64(len(b)) > s.opts.MaxBytes
	if overflow {
		d.trim(max(1, s.opts.MaxEntries*9/10), max(int64(1), s.opts.MaxBytes*9/10), true)
	}
	state := Failed
	if d.repair || overflow {
		encoded := make([][]byte, len(d.records))
		var total int64
		for i, r := range d.records {
			line, e := json.Marshal(r)
			if e != nil {
				err = e
				break
			}
			line = append(line, '\n')
			encoded[i] = line
			d.sizes[i] = int64(len(line))
			total += int64(len(line))
		}
		if total > s.opts.MaxBytes {
			d.trim(max(1, s.opts.MaxEntries*9/10), max(int64(1), s.opts.MaxBytes*9/10), true)
		}
		encoded = encoded[len(encoded)-len(d.records):]
		var data []byte
		for _, line := range encoded {
			data = append(data, line...)
		}
		if err == nil {
			err = s.replace(data)
		}
	} else {
		var f *os.File
		f, err = os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			err = privateFile(f)
			if err == nil {
				state = Unknown
				var n int
				n, err = s.write(f, b)
				if err == nil && n != len(b) {
					err = io.ErrShortWrite
				}
				if err == nil {
					err = s.syncFile(f)
				}
			}
			err = errors.Join(err, f.Close())
		}
	}
	if err != nil {
		s.cache = diskView{}
		settle(state, err)
		return
	}
	d.repair = false
	d.info, err = os.Stat(s.path)
	if err != nil {
		s.cache = diskView{}
		settle(Unknown, err)
		return
	}
	s.cache = d
	s.publish(d)
	// Capacity eviction is not a failed write and must never become local history.
	s.mu.Lock()
	local := s.local[:0]
	for _, e := range s.local {
		if e.sequence < ops[0].sequence || e.sequence > ops[len(ops)-1].sequence {
			local = append(local, e)
		} else {
			s.localBytes -= e.size
		}
	}
	if len(local) != len(s.local) {
		s.changed()
	}
	clear(s.local[len(local):])
	s.local = local
	s.mu.Unlock()
}
func (s *Store) clearDisk(op operation) {
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	f, err := s.acquire(ctx)
	if err == nil {
		err = s.replace(nil)
		release(f)
	}
	s.mu.Lock()
	s.clearing = false
	if err == nil {
		s.disk = nil
		local := s.local[:0]
		for _, e := range s.local {
			if e.sequence > op.sequence {
				local = append(local, e)
			} else {
				s.localBytes -= e.size
			}
		}
		clear(s.local[len(local):])
		s.local = local
		s.changed()
	} else {
		s.issue("", LocalOnly, Failed, err)
	}
	s.mu.Unlock()
	s.cache = diskView{}
	state := Synced
	if err != nil {
		state = Failed
	}
	op.clear.finish(ClearResult{state, err})
}
