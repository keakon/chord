package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/message"
)

func buildDirectoryInstructionView(initial directoryInstructionSnapshot, history []message.Message, binding directoryInstructionBinding) directoryInstructionView {
	v := directoryInstructionView{DynamicIndex: make(map[string]int)}
	latest := make(map[string]message.DirectoryInstructionFact)
	for i, m := range history {
		if m.Kind != message.KindDirectoryInstructions {
			continue
		}
		if err := message.ValidateDirectoryInstructions(m); err != nil {
			v.Err = fmt.Errorf("directory instructions message %d: %w", i, err)
			return v
		}
		for _, f := range m.DirectoryInstructions.Facts {
			if f.CheckoutRoot == binding.CheckoutRoot {
				latest[f.Scope] = f
				v.DynamicIndex[f.Scope] = i
			}
		}
	}
	for scope, o := range initial.Observations {
		f := directoryInstructionFactFromObservation(binding, scope, o)
		delete(latest, scope)
		delete(v.DynamicIndex, scope)
		switch f.Operation {
		case message.DirectoryInstructionUpsert:
			v.Initial = append(v.Initial, f)
		case message.DirectoryInstructionBlocked:
			v.Blocked = append(v.Blocked, f)
		}
	}
	message.SortDirectoryInstructionFacts(v.Initial)
	for _, f := range latest {
		switch f.Operation {
		case message.DirectoryInstructionUpsert:
			v.Dynamic = append(v.Dynamic, f)
		case message.DirectoryInstructionBlocked:
			v.Blocked = append(v.Blocked, f)
		}
	}
	message.SortDirectoryInstructionFacts(v.Dynamic)
	sort.SliceStable(v.Dynamic, func(i, j int) bool { return v.DynamicIndex[v.Dynamic[i].Scope] < v.DynamicIndex[v.Dynamic[j].Scope] })
	message.SortDirectoryInstructionFacts(v.Blocked)
	return v
}
func directoryInstructionProjection(facts []message.DirectoryInstructionFact) message.Message {
	m := message.Message{Role: message.RoleUser, Kind: message.KindDirectoryInstructionProjection, Content: message.RenderDirectoryInstructionFacts(facts)}
	for _, f := range facts {
		if f.Operation == message.DirectoryInstructionUpsert {
			m.DirectoryInstructionRefs = append(m.DirectoryInstructionRefs, f.Ref())
		}
	}
	m.DirectoryInstructionDigest = message.DirectoryInstructionBodyHash(m.Content)
	return m
}
func projectDirectoryInstructions(history []message.Message, v directoryInstructionView) ([]message.Message, error) {
	if v.Err != nil {
		return nil, v.Err
	}
	byIndex := make(map[int][]message.DirectoryInstructionFact)
	for _, f := range v.Dynamic {
		byIndex[v.DynamicIndex[f.Scope]] = append(byIndex[v.DynamicIndex[f.Scope]], f)
	}
	out := make([]message.Message, 0, len(history)+1)
	for i, m := range history {
		if m.Kind == message.KindDirectoryInstructions {
			if err := message.ValidateDirectoryInstructions(m); err != nil {
				return nil, err
			}
			if fs := byIndex[i]; len(fs) > 0 {
				out = append(out, directoryInstructionProjection(fs))
			}
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
func validateDirectoryInstructionRequest(messages []message.Message, v directoryInstructionView) (map[directoryInstructionVersion]struct{}, error) {
	if v.Err != nil {
		return nil, v.Err
	}
	want := make(map[directoryInstructionVersion]string)
	for _, fs := range [][]message.DirectoryInstructionFact{v.Initial, v.Dynamic} {
		for _, f := range fs {
			want[f.Ref()] = f.Body
		}
	}
	seen := make(map[directoryInstructionVersion]struct{}, len(want))
	for _, m := range messages {
		if m.Kind == message.KindDirectoryInstructions {
			return nil, fmt.Errorf("unprojected directory instruction fact in request")
		}
		for _, ref := range m.DirectoryInstructionRefs {
			body, ok := want[ref]
			if !ok {
				return nil, fmt.Errorf("stale directory instruction reference: %s", ref.Scope)
			}
			if !strings.Contains(m.Content, body) {
				return nil, fmt.Errorf("directory instruction body missing from request: %s", ref.Scope)
			}
			seen[ref] = struct{}{}
		}
	}
	for ref := range want {
		if _, ok := seen[ref]; !ok {
			return nil, fmt.Errorf("directory instruction reference missing from request: %s", ref.Scope)
		}
	}
	return seen, nil
}

// Candidate activation is an all-or-nothing transaction. Previously active
// scopes remain unchanged when a newly discovered batch exceeds input budget.
func directoryInstructionCandidateFacts(binding directoryInstructionBinding, observations map[string]directoryInstructionObservation, history []message.Message, inputBudget, estimatedInput int) []message.DirectoryInstructionFact {
	latest := make(map[string]message.DirectoryInstructionFact)
	for _, m := range history {
		if message.IsDirectoryInstructions(m) {
			for _, f := range m.DirectoryInstructions.Facts {
				if f.CheckoutRoot == binding.CheckoutRoot {
					latest[f.Scope] = f
				}
			}
		}
	}
	blocked := inputBudget <= 0 || estimatedInput+4096 > inputBudget
	var facts []message.DirectoryInstructionFact
	for scope, o := range observations {
		f := directoryInstructionFactFromObservation(binding, scope, o)
		old, exists := latest[scope]
		if f.Operation == message.DirectoryInstructionRemove && !exists {
			continue
		}
		if blocked && f.Operation == message.DirectoryInstructionUpsert {
			if exists && old.Operation == message.DirectoryInstructionUpsert && old.Ref() == f.Ref() {
				continue
			}
			f.Operation = message.DirectoryInstructionBlocked
			f.Reason = message.DirectoryInstructionReasonInputBudget
			f.Detail = "Directory rules exceed the available model input budget; use a larger context model or compact the conversation."
		}
		if exists && old == f {
			continue
		}
		facts = append(facts, f)
	}
	message.SortDirectoryInstructionFacts(facts)
	return facts
}
