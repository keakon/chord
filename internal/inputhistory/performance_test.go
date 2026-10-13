package inputhistory

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/atomicfile"
)

func reportInputHistoryP95(b *testing.B, samples []int64) {
	b.Helper()
	slices.Sort(samples)
	if len(samples) > 0 {
		b.ReportMetric(float64(samples[(len(samples)-1)*95/100]), "p95-ns")
	}
}
func BenchmarkInputHistoryTryRecord(b *testing.B) {
	s := New(b.TempDir(), DefaultOptions())
	defer s.Close(context.Background())
	r := historyRecord("sample", strings.Repeat("text ", 20))
	var samples []int64
	index := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		index++
		r.ID = fmt.Sprint(index)
		start := time.Now()
		s.TryRecord(r)
		if len(samples) < 10000 {
			samples = append(samples, time.Since(start).Nanoseconds())
		}
	}
	b.StopTimer()
	reportInputHistoryP95(b, samples)
}
func BenchmarkInputHistoryCapacityAppend(b *testing.B) {
	dir := b.TempDir()
	opts := DefaultOptions()
	var bytesWritten, rewrites int64
	s := newStore(dir, opts, fileHooks{write: func(f *os.File, data []byte) (int, error) { bytesWritten += int64(len(data)); return f.Write(data) }, replace: func(path string, data []byte) error {
		bytesWritten += int64(len(data))
		rewrites++
		return atomicfile.Replace(path, data, 0600)
	}})
	defer s.Close(context.Background())
	// Exercise the production encoder/writer against an almost-full real file.
	batch := make([]operation, 0, 32)
	for i := range 10000 {
		r := historyRecord(fmt.Sprint(i), strings.Repeat("text ", 600))
		batch = append(batch, operation{sequence: uint64(i + 1), record: &r})
		if len(batch) == 32 {
			s.encodeBatch(batch)
			batch = nil
		}
	}
	s.encodeBatch(batch)
	bytesWritten, rewrites = 0, 0
	var samples []int64
	index := 10000
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		index++
		r := historyRecord(fmt.Sprint(index), "new input")
		start := time.Now()
		s.encodeBatch([]operation{{sequence: uint64(index), record: &r}})
		if len(samples) < 10000 {
			samples = append(samples, time.Since(start).Nanoseconds())
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(bytesWritten)/float64(b.N), "written-B/op")
	b.ReportMetric(float64(rewrites), "rewrites")
	reportInputHistoryP95(b, samples)
	start := time.Now()
	_, err := s.Snapshot(context.Background(), true)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(time.Since(start).Nanoseconds()), "refresh-ns")
}
