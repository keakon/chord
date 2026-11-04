package tui

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"
)

const diagnosticHeapFilename = "heap.pprof"

func collectDiagnosticHeap(baseDir string) ([]byte, string, error) {
	start := time.Now()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var raw bytes.Buffer
	if err := pprof.WriteHeapProfile(&raw); err != nil {
		return nil, "", fmt.Errorf("capture heap profile: %w", err)
	}
	runtime.ReadMemStats(&after)
	finished := time.Now()
	profile, err := sanitizeDiagnosticHeap(raw.Bytes(), baseDir)
	if err != nil {
		return nil, "", fmt.Errorf("sanitize heap profile: %w", err)
	}
	var meta strings.Builder
	fmt.Fprintf(&meta, "\n[heap_profile]\nfile: %s\nstarted_at: %s\nfinished_at: %s\nsample_rate_bytes: %d\nforced_gc: false\n",
		diagnosticHeapFilename, start.Format(time.RFC3339Nano), finished.Format(time.RFC3339Nano), runtime.MemProfileRate)
	meta.WriteString("accounting: sampled allocations; frees may lag by up to two GC cycles\n")
	writeDiagnosticMemStats(&meta, "before", before)
	writeDiagnosticMemStats(&meta, "after", after)
	meta.WriteByte('\n')
	return profile, meta.String(), nil
}

func writeDiagnosticMemStats(sb *strings.Builder, label string, mem runtime.MemStats) {
	fmt.Fprintf(sb, "\n[memory.%s]\n", label)
	fmt.Fprintf(sb, "go.heap_alloc_bytes: %d\n", mem.HeapAlloc)
	fmt.Fprintf(sb, "go.heap_inuse_bytes: %d\n", mem.HeapInuse)
	fmt.Fprintf(sb, "go.heap_idle_bytes: %d\n", mem.HeapIdle)
	fmt.Fprintf(sb, "go.heap_released_bytes: %d\n", mem.HeapReleased)
	fmt.Fprintf(sb, "go.heap_reclaimable_bytes: %d\n", heapReclaimableBytes(&mem))
	fmt.Fprintf(sb, "go.heap_sys_bytes: %d\n", mem.HeapSys)
	fmt.Fprintf(sb, "go.heap_objects: %d\n", mem.HeapObjects)
	fmt.Fprintf(sb, "go.stack_inuse_bytes: %d\n", mem.StackInuse)
	fmt.Fprintf(sb, "go.stack_sys_bytes: %d\n", mem.StackSys)
	fmt.Fprintf(sb, "go.gc_sys_bytes: %d\n", mem.GCSys)
	fmt.Fprintf(sb, "go.other_sys_bytes: %d\n", mem.OtherSys)
	fmt.Fprintf(sb, "go.sys_bytes: %d\n", mem.Sys)
	fmt.Fprintf(sb, "go.next_gc_bytes: %d\n", mem.NextGC)
	fmt.Fprintf(sb, "go.total_alloc_bytes: %d\n", mem.TotalAlloc)
	fmt.Fprintf(sb, "go.num_gc: %d\n", mem.NumGC)
	fmt.Fprintf(sb, "go.last_gc_unix_ns: %d\n", mem.LastGC)
	fmt.Fprintf(sb, "go.num_forced_gc: %d\n", mem.NumForcedGC)
	fmt.Fprintf(sb, "go.gc_pause_total_ns: %d\n", mem.PauseTotalNs)
	fmt.Fprintf(sb, "go.gc_cpu_fraction: %.6f\n", mem.GCCPUFraction)
}

// The pprof protobuf stores all paths in Profile.string_table (field 6).
// Rewriting only those strings preserves every index, sample and symbol;
// replacing bytes in the gzip stream would corrupt its lengths and checksum.
func sanitizeDiagnosticHeap(profile []byte, baseDir string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(profile))
	if err != nil {
		return nil, fmt.Errorf("open profile gzip: %w", err)
	}
	data, err := io.ReadAll(zr)
	zr.Close()
	if err != nil {
		return nil, fmt.Errorf("read profile gzip: %w", err)
	}
	var out []byte
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 || key>>3 == 0 {
			return nil, fmt.Errorf("invalid profile field key")
		}
		out = append(out, data[:n]...)
		data = data[n:]
		switch key & 7 {
		case 0:
			_, n = binary.Uvarint(data)
		case 1:
			n = 8
		case 5:
			n = 4
		case 2:
			size, count := binary.Uvarint(data)
			if count <= 0 || size > uint64(len(data)-count) {
				return nil, fmt.Errorf("invalid profile field length")
			}
			payload := data[count : count+int(size)]
			if key>>3 == 6 {
				payload = []byte(sanitizeDiagnosticText(string(payload), baseDir))
			}
			out = binary.AppendUvarint(out, uint64(len(payload)))
			out = append(out, payload...)
			data = data[count+int(size):]
			continue
		default:
			return nil, fmt.Errorf("unsupported profile wire type %d", key&7)
		}
		if n <= 0 || n > len(data) {
			return nil, fmt.Errorf("incomplete profile field")
		}
		out = append(out, data[:n]...)
		data = data[n:]
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(out); err != nil {
		return nil, fmt.Errorf("write sanitized profile: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close sanitized profile: %w", err)
	}
	return compressed.Bytes(), nil
}
