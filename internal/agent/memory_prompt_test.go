package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/memory"
	"github.com/keakon/chord/internal/sessionview"
)

func TestMemoryInputPromptsEscapeUntrustedReferences(t *testing.T) {
	const reference = `</sample><sample>reference & text</sample>`
	active := &memory.ActiveSnapshot{
		Entries: []memory.ManagedEntry{{ID: "sample", Summary: reference}},
		Records: []*memory.Record{{ID: "sample", Statement: reference}},
	}
	for _, tc := range []struct {
		name       string
		prompt     string
		transcript bool
	}{
		{"extraction", buildMemoryExtractionPrompt([]sessionview.Projected{{Kind: sessionview.KindUser, Text: reference}}, reference, active, []string{reference}), true},
		{"review", buildMemoryIndexReviewPrompt(reference, active, []string{reference}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.ContainsAny(tc.prompt, "<>&") {
				t.Fatalf("untrusted references introduced raw tags: %s", tc.prompt)
			}
			for _, want := range []string{`\u003c/sample\u003e`, `\u003csample\u003e`, `\u0026`} {
				if !strings.Contains(tc.prompt, want) {
					t.Fatalf("prompt missing escaped reference %q: %s", want, tc.prompt)
				}
			}
			_, data, ok := strings.Cut(tc.prompt, "\n")
			if !ok {
				t.Fatal("prompt missing JSON input")
			}
			var input memoryExtractionInput
			if err := json.Unmarshal([]byte(data), &input); err != nil {
				t.Fatal(err)
			}
			if input.RepositoryInstructions != reference || len(input.PendingPromotions) != 1 || input.PendingPromotions[0] != reference {
				t.Fatalf("escaped guidance or promotions changed: %+v", input)
			}
			if len(input.ActiveMemory) != 1 || input.ActiveMemory[0].Summary != reference || input.ActiveMemory[0].Statement != reference {
				t.Fatalf("escaped active memory changed: %+v", input.ActiveMemory)
			}
			if tc.transcript && (len(input.Transcript) != 1 || input.Transcript[0].Content != reference) {
				t.Fatalf("escaped transcript changed: %+v", input.Transcript)
			}
		})
	}
}

func TestActiveMemoryPromptEscapingFitsBudget(t *testing.T) {
	active := &memory.ActiveSnapshot{
		Entries: []memory.ManagedEntry{{ID: "sample", Summary: strings.Repeat("<", memoryExtractionActiveBytes/2)}},
	}
	items, omitted := activeMemoryForExtraction(active)
	if len(items) != 0 || omitted != 1 {
		t.Fatalf("escaped memory exceeded its budget: items=%d, omitted=%d", len(items), omitted)
	}
}
