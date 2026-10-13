package contextdiag

import "testing"

func TestStoreAttemptOwnershipAndImmutability(t *testing.T) {
	var s Store
	binding := Binding{TaskID: "main", Epoch: 1}
	id := s.BeginRequest(binding, 3)
	id.Attempt = 1
	snapshot := Snapshot{ID: id, State: Dispatched, Categories: []Category{{Sources: []Source{{Label: "sample"}}}}, Reduction: Reduction{Reasons: []string{"sample"}}, Cache: Cache{LocalChanges: []string{System}}}
	if !s.Publish(snapshot) {
		t.Fatal("first submission rejected")
	}
	snapshot.Categories[0].Sources[0].Label = "mutated"
	got, _ := s.Latest()
	if got.Categories[0].Sources[0].Label != "sample" {
		t.Fatal("input alias")
	}
	got.Reduction.Reasons[0] = "mutated"
	got.Cache.LocalChanges[0] = "mutated"
	got, _ = s.Latest()
	if got.Reduction.Reasons[0] != "sample" || got.Cache.LocalChanges[0] != System {
		t.Fatal("output alias")
	}
	next := s.BeginRequest(binding, 4)
	if got, _ = s.Latest(); got.ID != id {
		t.Fatal("unsubmitted request replaced latest")
	}
	if s.Finish(id, Completed, Usage{State: Reported}) {
		t.Fatal("old sequence finished")
	}
	id.Attempt++
	snapshot.ID = id
	if s.Publish(snapshot) {
		t.Fatal("old sequence published")
	}
	next.Attempt = 1
	snapshot.ID = next
	if !s.Publish(snapshot) {
		t.Fatal("new sequence rejected")
	}
	if !s.Finish(next, Completed, Usage{State: Reported, CacheRead: 10}) || s.Finish(next, Failed, Usage{}) {
		t.Fatal("terminal transition not idempotent")
	}
	got, _ = s.Latest()
	revision := got.Revision
	if got.Cache.Explanation != ReadObserved {
		t.Fatal("usage explanation")
	}
	s.Invalidate(binding)
	snapshot.ID.Attempt++
	if s.Publish(snapshot) || s.Finish(next, Failed, Usage{}) {
		t.Fatal("invalidated publisher accepted")
	}
	got, _ = s.Latest()
	if got.Revision != revision || got.State != Completed {
		t.Fatal("invalidation modified read-only snapshot")
	}
	fresh := Store{}
	binding.Epoch++
	freshID := fresh.BeginRequest(binding, 0)
	freshID.Attempt = 1
	snapshot.ID = freshID
	if !fresh.Publish(snapshot) {
		t.Fatal("new runtime rejected")
	}
}

func TestStoreRejectsOlderAttemptAndWrongBinding(t *testing.T) {
	var s Store
	b := Binding{Epoch: 1}
	id := s.BeginRequest(b, 0)
	id.Attempt = 2
	if !s.Publish(Snapshot{ID: id, State: Dispatched}) {
		t.Fatal("publish")
	}
	old := id
	old.Attempt = 1
	if s.Publish(Snapshot{ID: old}) || s.Finish(old, Completed, Usage{}) {
		t.Fatal("older attempt accepted")
	}
	old = id
	old.Binding.Epoch++
	if s.Publish(Snapshot{ID: old}) {
		t.Fatal("other binding accepted")
	}
}

func TestDisplayLabelBoundsAndRemovesControls(t *testing.T) {
	label := ""
	for range 100 {
		label += "界"
	}
	label = "\n\x1b" + label
	got := DisplayLabel(label)
	if len([]rune(got)) != 80 || len(got) != 240 {
		t.Fatal("label not bounded by runes")
	}
}
