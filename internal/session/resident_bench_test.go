package session

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"runtime"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// The benchmarks in this file answer a different question from the existing
// BenchmarkExportedSessionToMessagesLargeSession: not "how many bytes does
// building a history allocate?" but "how many bytes does a live session keep
// alive after a full GC?".
//
// resident = HeapAlloc delta measured across runtime.GC() while the measured
// value is still reachable (pinned in a package-level sink and KeepAlive'd).
// Allocations that die before the GC are reported by allocs/op instead; the two
// numbers diverge sharply once content and attachment payloads are per-message.

// residentBenchSink pins the value under measurement so the collector cannot
// reclaim it before ReadMemStats, which would otherwise make the delta read 0.
var residentBenchSink any

// measureResidentHeap reports the heap bytes still live after a full GC that
// were retained by build's result.
func measureResidentHeap(b *testing.B, build func() any) int64 {
	b.Helper()
	residentBenchSink = nil
	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	value := build()
	residentBenchSink = value
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(value)
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	residentBenchSink = nil
	return delta
}

const residentAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 .,;:-_/"

// residentContent returns a fresh string of exactly size bytes. Every message
// gets its own allocation and its own random tail: sharing one string (as the
// older benchmark did) measures structure only, never resident content.
func residentContent(r *rand.Rand, size, index int) string {
	buf := make([]byte, size)
	for i := 0; i+8 <= size; i += 8 {
		v := r.Uint64()
		for j := range 8 {
			buf[i+j] = residentAlphabet[byte(v>>(8*j))%byte(len(residentAlphabet))]
		}
	}
	// Embed the message index so no two messages can share content even if the
	// random tail happened to repeat.
	copy(buf, fmt.Sprintf("message-%06d ", index))
	return string(buf)
}

// residentAttachment allocates a fresh payload per message. Reusing one []byte
// across messages would report the attachment once instead of once per message.
func residentAttachment(r *rand.Rand, size int) []byte {
	data := make([]byte, size)
	for i := 0; i+8 <= size; i += 8 {
		binary.LittleEndian.PutUint64(data[i:], r.Uint64())
	}
	return data
}

// residentAttachmentCap bounds how many messages in one history carry an
// attachment. Attaching a payload to every message at the 8000 x 2 MiB corner
// would need 16 GiB and OOM the machine; small histories still attach to every
// message, larger ones spread the same cap over a wider stride.
const residentAttachmentCap = 200

func residentAttachmentStride(messageCount int) int {
	if messageCount <= residentAttachmentCap {
		return 1
	}
	return (messageCount + residentAttachmentCap - 1) / residentAttachmentCap
}

// buildResidentHistory returns a session history in which every message owns an
// independent content string and, when attachmentBytes > 0, its own payload.
// Messages carrying an attachment are user messages, since that is where image
// parts come from.
func buildResidentHistory(messageCount, contentBytes, attachmentBytes int, seed uint64) []message.Message {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	stride := residentAttachmentStride(messageCount)
	history := make([]message.Message, messageCount)
	for i := range history {
		role := message.RoleAssistant
		switch i % 3 {
		case 0:
			role = message.RoleUser
		case 2:
			role = message.RoleTool
		}
		attach := attachmentBytes > 0 && i%stride == 0
		if attach {
			role = message.RoleUser
		}
		msg := message.Message{
			Role:    role,
			Content: residentContent(r, contentBytes, i),
		}
		if attach {
			msg.Parts = []message.ContentPart{{
				Type:     message.ContentPartImage,
				MimeType: "image/png",
				FileName: fmt.Sprintf("shot-%06d.png", i),
				Data:     residentAttachment(r, attachmentBytes),
			}}
		}
		history[i] = msg
	}
	return history
}

func residentSizeLabel(bytes int) string {
	switch {
	case bytes == 0:
		return "none"
	case bytes%(1<<20) == 0:
		return fmt.Sprintf("%dMB", bytes>>20)
	case bytes%(1<<10) == 0:
		return fmt.Sprintf("%dKB", bytes>>10)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// BenchmarkSessionResidentHeap answers: how much heap does one session's
// in-memory history keep alive after GC, for a given message count, content
// size and attachment tier? Content and payloads are allocated per message.
func BenchmarkSessionResidentHeap(b *testing.B) {
	for _, messageCount := range []int{200, 2000, 8000} {
		for _, contentBytes := range []int{200, 2 << 10, 8 << 10} {
			for _, attachmentBytes := range []int{0, 64 << 10, 512 << 10, 2 << 20} {
				name := fmt.Sprintf("msgs_%d/content_%s/attach_%s",
					messageCount, residentSizeLabel(contentBytes), residentSizeLabel(attachmentBytes))
				b.Run(name, func(b *testing.B) {
					seed := uint64(messageCount)*31 + uint64(contentBytes)*7 + uint64(attachmentBytes)
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						delta := measureResidentHeap(b, func() any {
							return buildResidentHistory(messageCount, contentBytes, attachmentBytes, seed)
						})
						b.ReportMetric(float64(delta)/1024/1024, "MiB/resident")
					}
				})
			}
		}
	}
}

// BenchmarkMultiSessionResident answers: what does keeping several sessions
// (several MainAgent instances) resident at once cost, and does the cost scale
// linearly? Each session is an independent 2000-message history.
func BenchmarkMultiSessionResident(b *testing.B) {
	const (
		messagesPerSession = 2000
		contentBytes       = 2 << 10
		attachmentBytes    = 64 << 10
	)
	for _, sessions := range []int{3, 8} {
		b.Run(fmt.Sprintf("sessions_%d", sessions), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				delta := measureResidentHeap(b, func() any {
					all := make([][]message.Message, sessions)
					for s := range all {
						all[s] = buildResidentHistory(messagesPerSession, contentBytes, attachmentBytes, uint64(s+1)*0x9e3779b1)
					}
					return all
				})
				b.ReportMetric(float64(delta)/1024/1024, "MiB/resident")
			}
		})
	}
}
