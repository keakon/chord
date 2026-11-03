package lsp

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"
)

// FileWriteStatus reports whether the diagnostics for one written file
// describe that write.
type FileWriteStatus uint8

const (
	// FileWriteStatusReported means a fresh publish or a synchronous backend
	// produced the file's diagnostics for this write.
	FileWriteStatusReported FileWriteStatus = iota
	// FileWriteStatusPartial means fresh diagnostics arrived, but some owning
	// servers missed the content synchronization.
	FileWriteStatusPartial
	// FileWriteStatusTimeout means the diagnostics wait ended without a fresh
	// publish.
	FileWriteStatusTimeout
	// FileWriteStatusSyncFailed means no server accepted the content sync.
	FileWriteStatusSyncFailed
	// FileWriteStatusStartFailed means no client became available.
	FileWriteStatusStartFailed
	// FileWriteStatusCancelled means the tool call was cancelled first.
	FileWriteStatusCancelled
	// FileWriteStatusBackendUnavailable means a selected diagnostic backend
	// could not produce a result.
	FileWriteStatusBackendUnavailable
	// FileWriteStatusNoServer means no configured server covers the file.
	FileWriteStatusNoServer
)

// FileWriteResult is one file's after-write outcome. Detail names the failure
// when Status is neither Reported nor NoServer.
type FileWriteResult struct {
	Output        string
	Status        FileWriteStatus
	Detail        string
	FailedServers []string
}

// FileWriteDiagnosticsState is the batch formatter's view of one file's
// diagnostics freshness.
type FileWriteDiagnosticsState struct {
	Status        FileWriteStatus
	Detail        string
	FailedServers []string
}

// needsFileReport reports whether the batch formatter should name this file
// even when it has no diagnostics to show.
func (s FileWriteDiagnosticsState) needsFileReport() bool {
	return s.Status != FileWriteStatusReported && s.Status != FileWriteStatusNoServer
}

func (s FileWriteDiagnosticsState) reason() string {
	if detail := strings.TrimSpace(s.Detail); detail != "" {
		return strings.TrimSuffix(detail, ".")
	}
	switch s.Status {
	case FileWriteStatusPartial:
		return "some language servers did not verify this write"
	case FileWriteStatusTimeout:
		return "diagnostics wait timed out"
	case FileWriteStatusSyncFailed:
		return "file synchronization failed"
	case FileWriteStatusStartFailed:
		return "language server unavailable"
	case FileWriteStatusCancelled:
		return "cancelled"
	case FileWriteStatusBackendUnavailable:
		return "Python diagnostics backend unavailable"
	default:
		return "diagnostics were not verified"
	}
}

func reportedFileWrite(output string) FileWriteResult {
	return FileWriteResult{Output: output, Status: FileWriteStatusReported}
}

func synchronizedFileWrite(output string, sync afterWriteSync) FileWriteResult {
	if len(sync.serverErrors) == 0 {
		return reportedFileWrite(output)
	}
	return FileWriteResult{
		Output:        output,
		Status:        FileWriteStatusPartial,
		Detail:        "file synchronization failed: " + formatServerErrors(sync.serverErrors),
		FailedServers: slices.Sorted(maps.Keys(sync.serverErrors)),
	}
}

// unavailableStatus maps a failed stage to Cancelled when the tool call was
// cancelled while it ran.
func unavailableStatus(ctx context.Context, status FileWriteStatus) FileWriteStatus {
	if ctx != nil && ctx.Err() != nil {
		return FileWriteStatusCancelled
	}
	return status
}

func fileWriteStatusDetail(status FileWriteStatus) string {
	switch status {
	case FileWriteStatusSyncFailed:
		return "file synchronization failed"
	case FileWriteStatusStartFailed:
		return "language server unavailable"
	case FileWriteStatusCancelled:
		return "cancelled"
	default:
		return ""
	}
}

func unavailableFileWrite(ctx context.Context, output string, status FileWriteStatus) FileWriteResult {
	status = unavailableStatus(ctx, status)
	return FileWriteResult{Output: output, Status: status, Detail: fileWriteStatusDetail(status)}
}

func timeoutFileWrite(output string, timeout time.Duration) FileWriteResult {
	return FileWriteResult{
		Output: output,
		Status: FileWriteStatusTimeout,
		// The write's late watch catches whatever the server publishes for
		// this version after the wait gave up; say so, or an unverified file
		// looks like a dead end. The precise wait length stays in the logs;
		// in the result text it only varies card to card.
		Detail: diagnosticsWaitDetail(timeout) + "; eligible versioned diagnostics may follow with the next request",
	}
}

// diagnosticsWaitDetail names a diagnostics wait that ran out. The timeout
// value itself is logged at the wait site and is not repeated here: it is an
// internal budget leftover no reader can act on.
func diagnosticsWaitDetail(timeout time.Duration) string {
	if timeout <= 0 {
		return "wait budget exhausted"
	}
	return "wait timed out"
}
