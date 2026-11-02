package agent

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// These benchmarks measure both the request snapshot and the complete
// prepareMessagesForLLM path, including how much heap each returned surface
// keeps alive and how much of it is the copy of inline attachment bytes.
//
// resident = HeapAlloc delta across runtime.GC() while the snapshot is still
// reachable. b.ReportAllocs() alone cannot answer this: allocation volume is
// reclaimed by the GC, residence is what stays. message.Message.Clone
// (message/clone.go:30) deep-copies binary ContentPart.Data, so an inline
// attachment is duplicated once per Snapshot; a path-only part (Data nil,
// ImagePath set, DataBytes recorded) costs only structure.
const (
	requestSurfaceMessages        = 1200
	requestSurfaceAttachments     = 60
	requestSurfaceAttachmentBytes = 1 << 20
)

// requestSurfaceBenchSink pins the measured value across the GC.
var requestSurfaceBenchSink any

func measureRequestSurfaceResident(b *testing.B, build func() any) int64 {
	b.Helper()
	requestSurfaceBenchSink = nil
	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	value := build()
	requestSurfaceBenchSink = value
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(value)
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	requestSurfaceBenchSink = nil
	return delta
}

// buildRequestSurfaceFixture returns the same logical history twice: once with
// attachment bytes held inline (Data set) and once with them already persisted
// (Data nil, ImagePath + DataBytes set). Every payload is allocated
// independently; the two passes differ only in whether the manager keeps the
// bytes in memory.
func buildRequestSurfaceFixture(inlineData bool) []message.Message {
	r := rand.New(rand.NewPCG(0x5eed, 0xbeef))
	history := make([]message.Message, requestSurfaceMessages)
	attachEvery := requestSurfaceMessages / requestSurfaceAttachments
	for i := range history {
		role := message.RoleAssistant
		if i%2 == 0 {
			role = message.RoleUser
		}
		msg := message.Message{
			Role: role,
			// Fresh string per message, mirroring a real transcript.
			Content: fmt.Sprintf("turn %04d\n%s", i, strings.Repeat("context line from the workspace\n", 20)),
		}
		if i%attachEvery == 0 {
			payload := make([]byte, requestSurfaceAttachmentBytes)
			for off := 0; off+8 <= len(payload); off += 8 {
				binary.LittleEndian.PutUint64(payload[off:], r.Uint64())
			}
			part := message.ContentPart{
				Type:     message.ContentPartImage,
				MimeType: "image/png",
				FileName: fmt.Sprintf("shot-%03d.png", i),
			}
			if inlineData {
				part.Data = payload
			} else {
				part.ImagePath = fmt.Sprintf("/tmp/chord-bench/shot-%03d.png", i)
				part.DataBytes = int64(len(payload))
			}
			msg.Parts = []message.ContentPart{part}
		}
		history[i] = msg
	}
	return history
}

// BenchmarkRequestSurfaceSnapshotResident answers: what does one
// ctxmgr.Manager.Snapshot() of a 1200-message, 60-attachment history retain
// after GC — with attachment bytes inline versus already path-only?
func BenchmarkRequestSurfaceSnapshotResident(b *testing.B) {
	for _, inlineData := range []bool{true, false} {
		name := "path_only"
		if inlineData {
			name = "inline_data"
		}
		b.Run(name, func(b *testing.B) {
			history := buildRequestSurfaceFixture(inlineData)
			manager := ctxmgr.NewManager(1_000_000, 0.8)
			manager.RestoreMessages(history)
			requestSurfaceBenchSink = manager

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				delta := measureRequestSurfaceResident(b, func() any {
					return manager.Snapshot()
				})
				b.ReportMetric(float64(delta)/1024/1024, "MiB/resident")
				runtime.KeepAlive(manager)
			}
		})
	}
}

// BenchmarkRequestSurfacePreparedResident adds the real request-preparation
// pass after Snapshot so the retained heap includes the surface dispatched to
// the provider, not only the cloned context history.
func BenchmarkRequestSurfacePreparedResident(b *testing.B) {
	for _, inlineData := range []bool{true, false} {
		name := "path_only"
		if inlineData {
			name = "inline_data"
		}
		b.Run(name, func(b *testing.B) {
			history := buildRequestSurfaceFixture(inlineData)
			manager := ctxmgr.NewManager(1_000_000, 0.8)
			manager.RestoreMessages(history)
			agent := newContextPolicyTestAgent("")
			agent.ctxMgr = manager
			requestSurfaceBenchSink = manager

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				delta := measureRequestSurfaceResident(b, func() any {
					return agent.prepareMessagesForLLM(manager.Snapshot())
				})
				b.ReportMetric(float64(delta)/1024/1024, "MiB/resident")
				runtime.KeepAlive(manager)
			}
		})
	}
}
