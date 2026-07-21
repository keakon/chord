package tools

import (
	"context"
	"sync"
)

// DeleteAudit reports the canonical paths affected by one Delete execution.
// It is delivered through the execution context so runtime bookkeeping does
// not need to parse the model-visible, possibly relative-path result text.
type DeleteAudit struct {
	Deleted       []string
	AlreadyAbsent []string
	Blocked       []string
	Failed        []string
	NotAttempted  []string
}

// DeleteAuditSink receives structured Delete execution metadata.
type DeleteAuditSink interface {
	SetDeleteAudit(DeleteAudit)
}

// DeleteAuditCollector is scoped to one tool execution through context.
type DeleteAuditCollector struct {
	mu    sync.Mutex
	audit DeleteAudit
}

func (c *DeleteAuditCollector) SetDeleteAudit(audit DeleteAudit) {
	if c == nil {
		return
	}
	audit.Deleted = cloneDeleteAuditPaths(audit.Deleted)
	audit.AlreadyAbsent = cloneDeleteAuditPaths(audit.AlreadyAbsent)
	audit.Blocked = cloneDeleteAuditPaths(audit.Blocked)
	audit.Failed = cloneDeleteAuditPaths(audit.Failed)
	audit.NotAttempted = cloneDeleteAuditPaths(audit.NotAttempted)
	c.mu.Lock()
	c.audit = audit
	c.mu.Unlock()
}

func (c *DeleteAuditCollector) Audit() (DeleteAudit, bool) {
	if c == nil {
		return DeleteAudit{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.audit.Deleted) == 0 && len(c.audit.AlreadyAbsent) == 0 && len(c.audit.Blocked) == 0 && len(c.audit.Failed) == 0 && len(c.audit.NotAttempted) == 0 {
		return DeleteAudit{}, false
	}
	return DeleteAudit{
		Deleted:       cloneDeleteAuditPaths(c.audit.Deleted),
		AlreadyAbsent: cloneDeleteAuditPaths(c.audit.AlreadyAbsent),
		Blocked:       cloneDeleteAuditPaths(c.audit.Blocked),
		Failed:        cloneDeleteAuditPaths(c.audit.Failed),
		NotAttempted:  cloneDeleteAuditPaths(c.audit.NotAttempted),
	}, true
}

func cloneDeleteAuditPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	return append([]string(nil), paths...)
}

// WithDeleteAuditSink returns a context carrying sink. A nil sink leaves the
// context unchanged.
func WithDeleteAuditSink(ctx context.Context, sink DeleteAuditSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, deleteAuditSinkKey, sink)
}

func deleteAuditSinkFromContext(ctx context.Context) (DeleteAuditSink, bool) {
	if ctx == nil {
		return nil, false
	}
	sink, ok := ctx.Value(deleteAuditSinkKey).(DeleteAuditSink)
	return sink, ok
}
