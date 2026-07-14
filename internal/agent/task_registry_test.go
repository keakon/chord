package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestWriteScopesOverlapMatchesExactAndNestedPathsOnly(t *testing.T) {
	tests := []struct {
		name string
		a    tools.WriteScope
		b    tools.WriteScope
		want bool
	}{
		{
			name: "same file overlaps",
			a:    tools.WriteScope{Files: []string{"internal/foo/bar.go"}},
			b:    tools.WriteScope{Files: []string{"internal/foo/bar.go"}},
			want: true,
		},
		{
			name: "file under path prefix overlaps",
			a:    tools.WriteScope{Files: []string{"internal/foo/bar.go"}},
			b:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			want: true,
		},
		{
			name: "nested path prefixes overlap",
			a:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			b:    tools.WriteScope{PathPrefix: []string{"internal/foo/bar"}},
			want: true,
		},
		{
			name: "prefix-like sibling names do not overlap",
			a:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			b:    tools.WriteScope{Files: []string{"internal/foobar/baz.go"}},
			want: false,
		},
		{
			name: "path prefixes require boundary",
			a:    tools.WriteScope{PathPrefix: []string{"pkg/mod"}},
			b:    tools.WriteScope{PathPrefix: []string{"pkg/module"}},
			want: false,
		},
		{
			name: "readonly never overlaps",
			a:    tools.WriteScope{ReadOnly: true, PathPrefix: []string{"internal/foo"}},
			b:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := writeScopesOverlap(tc.a, tc.b)
			if got != tc.want {
				t.Fatalf("writeScopesOverlap(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestPathContainsPathUsesPathBoundaries(t *testing.T) {
	if !pathContainsPath("internal/foo", "internal/foo/bar.go") {
		t.Fatal("expected nested path to match")
	}
	if pathContainsPath("internal/foo", "internal/foobar/bar.go") {
		t.Fatal("did not expect prefix-like sibling path to match")
	}
	if !pathContainsPath("internal/foo", "internal/foo") {
		t.Fatal("expected identical path to match")
	}
}

func TestMergeDurableTaskRecordsPreservesCoordinationIdentity(t *testing.T) {
	base := map[string]*DurableTaskRecord{
		"adhoc-identity": {
			TaskID:             "adhoc-identity",
			PlanTaskRef:        "plan-item-8",
			SemanticTaskKey:    "restore-identity",
			ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
			State:              string(SubAgentStateCompleted),
		},
	}
	extra := map[string]*DurableTaskRecord{
		"adhoc-identity": {
			TaskID: "adhoc-identity",
			State:  string(SubAgentStateIdle),
		},
	}

	got := mergeDurableTaskRecords(base, extra)["adhoc-identity"]
	if got.PlanTaskRef != "plan-item-8" || got.SemanticTaskKey != "restore-identity" {
		t.Fatalf("merged task identity = (%q, %q), want durable values", got.PlanTaskRef, got.SemanticTaskKey)
	}
	if len(got.ExpectedWriteScope.PathPrefix) != 1 || got.ExpectedWriteScope.PathPrefix[0] != "internal/agent" {
		t.Fatalf("merged write scope = %#v, want durable path prefix", got.ExpectedWriteScope)
	}
}

func TestPersistTaskRegistrySerializesSnapshotOrder(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-persist-order": {
			TaskID: "adhoc-persist-order",
			State:  string(SubAgentStateIdle),
		},
	})
	firstSnapshot := make(chan struct{})
	releaseFirstWrite := make(chan struct{})
	var hookCalls atomic.Int32
	a.taskRegistryPersistHook = func() {
		if hookCalls.Add(1) == 1 {
			close(firstSnapshot)
			<-releaseFirstWrite
		}
	}

	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		a.persistTaskRegistry()
	}()
	<-firstSnapshot
	a.subs.mu.Lock()
	a.subs.taskRecords["adhoc-persist-order"].State = string(SubAgentStateCompleted)
	a.subs.mu.Unlock()
	secondDone := make(chan struct{})
	writers.Add(1)
	go func() {
		defer writers.Done()
		a.persistTaskRegistry()
		close(secondDone)
	}()
	select {
	case <-secondDone:
		t.Fatal("newer registry write bypassed the in-flight older snapshot")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirstWrite)
	writers.Wait()

	records, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	if got := records["adhoc-persist-order"]; got == nil || got.State != string(SubAgentStateCompleted) {
		t.Fatalf("persisted record = %#v, want newest completed state", got)
	}
}
