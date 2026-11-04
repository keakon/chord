package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const diagnosticLogScanBytes = 16 << 20
const diagnosticLogChunkBytes = 64 << 10

const diagnosticLogFieldValue = `(?:"(?:\\.|[^"\\])*"|[^\s"]+)`

// Context fields follow cmd/chord's formatter order. Quoted values and bare
// paths may contain ']', so only the complete field grammar closes the header.
var diagnosticLogHeader = regexp.MustCompile(
	`^\[[A-Z] \d{4}-\d\d-\d\d \d\d:\d\d:\d\d [^\s\]]+` +
		`(?: pwd=` + diagnosticLogFieldValue + `)?` +
		`(?: pid=(\d+))?` +
		`(?: sid=` + diagnosticLogFieldValue + `)?` +
		`(?: agent=` + diagnosticLogFieldValue + `)?\]`)

type diagnosticLogTail struct {
	tail          string
	sources       []string
	scanned       int64
	scanLimited   bool
	outputLimited bool
	errors        []string
}

func (r diagnosticLogTail) metadata(baseDir string) string {
	status := "not_found"
	if r.tail != "" {
		status = "available"
	}
	if len(r.errors) > 0 {
		status = "error"
		if r.tail != "" {
			status = "partial"
		}
	} else if r.scanLimited {
		status = "scan_limit"
	}
	return sanitizeDiagnosticText(fmt.Sprintf(
		"\n[runtime_log]\nruntime_log_status: %s\nruntime_log_sources: %s\nruntime_log_scanned_bytes: %d\nruntime_log_scan_limited: %t\nruntime_log_output_limited: %t\nruntime_log_errors: %q\nruntime_log_collected_at: %s\n",
		status, strings.Join(r.sources, ", "), r.scanned, r.scanLimited, r.outputLimited, r.errors, time.Now().Format(time.RFC3339Nano)), baseDir)
}

func isRuntimeLogFile(name string) bool {
	if name == "chord.log" {
		return true
	}
	suffix, ok := strings.CutPrefix(name, "chord.log.")
	n, err := strconv.Atoi(suffix)
	return ok && err == nil && n > 0
}

func diagnosticLogPID(line string) (int, bool) {
	header := diagnosticLogHeader.FindStringSubmatch(line)
	if header == nil {
		return 0, false
	}
	pid, _ := strconv.Atoi(header[1])
	return pid, true
}

func collectDiagnosticLogTail(ctx context.Context, logsDir, baseDir string, pid int) diagnosticLogTail {
	var result diagnosticLogTail
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			result.errors = append(result.errors, err.Error())
		}
		return result
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && isRuntimeLogFile(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		rotation := func(name string) int {
			n, _ := strconv.Atoi(strings.TrimPrefix(name, "chord.log."))
			return n
		}
		return rotation(a) - rotation(b)
	})
	// Scan newest first, retaining a bounded record until its header establishes
	// ownership. The record may straddle a rotation boundary.
	var pending, selected []string
	pendingBytes, selectedBytes := 0, 0
	pendingLimited := false
	for _, name := range names {
		path := filepath.Join(logsDir, name)
		err := scanDiagnosticLogReverse(ctx, path, &result, func(line string, truncated bool) bool {
			recordPID, header := diagnosticLogPID(line)
			if !header {
				if len(pending) < diagnosticLogTailLines && pendingBytes+len(line)+1 <= diagnosticLogTailBytes {
					pending = append(pending, line)
					pendingBytes += len(line) + 1
				} else {
					pendingLimited = true
				}
				pendingLimited = pendingLimited || truncated
				return true
			}
			if recordPID == pid {
				if !slices.Contains(result.sources, name) {
					result.sources = append(result.sources, name)
				}
				pending = append(pending, line)
				for _, s := range pending {
					if len(selected) >= diagnosticLogTailLines || selectedBytes+len(s)+1 > diagnosticLogTailBytes {
						result.outputLimited = true
						break
					}
					selected = append(selected, s)
					selectedBytes += len(s) + 1
				}
				result.outputLimited = result.outputLimited || pendingLimited || truncated
			}
			pending = nil
			pendingBytes = 0
			pendingLimited = false
			return !result.outputLimited
		})
		if err != nil {
			result.errors = append(result.errors, err.Error())
			// A missing segment makes continuation ownership unknowable.
			pending = nil
			pendingBytes = 0
			pendingLimited = false
		}
		if ctx.Err() != nil || result.scanLimited || result.outputLimited {
			break
		}
	}
	slices.Reverse(selected)
	if len(selected) > 0 {
		result.tail = sanitizeDiagnosticText(strings.Join(selected, "\n")+"\n", baseDir)
	}
	return result
}

func scanDiagnosticLogReverse(ctx context.Context, path string, result *diagnosticLogTail, visit func(string, bool) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open runtime log: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat runtime log: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("runtime log %s is not a regular file", path)
	}
	end := info.Size()
	buf := make([]byte, diagnosticLogChunkBytes)
	var suffix []byte
	truncated, atEnd := false, true
	for end > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("scan runtime log: %w", err)
		}
		size := min(end, int64(len(buf)), diagnosticLogScanBytes-result.scanned)
		if size == 0 {
			result.scanLimited = true
			return nil
		}
		start := end - size
		n, err := f.ReadAt(buf[:size], start)
		result.scanned += int64(n)
		if err != nil {
			return fmt.Errorf("read runtime log: %w", err)
		}
		chunk := buf[:n]
		for len(chunk) > 0 {
			idx := bytes.LastIndexByte(chunk, '\n')
			part := chunk[idx+1:]
			if len(part)+len(suffix) > diagnosticLogTailBytes-32 {
				truncated = true
			}
			// Keep the prefix of an oversized line, so its header can still
			// establish ownership without an unbounded allocation.
			joined := make([]byte, min(len(part)+len(suffix), diagnosticLogTailBytes-32))
			copied := copy(joined, part)
			copy(joined[copied:], suffix)
			suffix = joined
			if idx < 0 {
				break
			}
			if !(atEnd && len(suffix) == 0) {
				line := strings.TrimSuffix(string(suffix), "\r")
				if truncated {
					line += " [line truncated]"
				}
				if !visit(line, truncated) {
					return nil
				}
			}
			atEnd = false
			suffix = nil
			truncated = false
			chunk = chunk[:idx]
		}
		end = start
	}
	if len(suffix) > 0 {
		line := strings.TrimSuffix(string(suffix), "\r")
		if truncated {
			line += " [line truncated]"
		}
		visit(line, truncated)
	}
	return nil
}
