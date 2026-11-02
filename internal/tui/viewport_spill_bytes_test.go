package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// spillBytesPayloadBlock returns a block whose large fields all belong to the
// payload set spillBlock releases.
func spillBytesPayloadBlock(id int) *Block {
	return &Block{
		ID:                    id,
		Type:                  BlockToolCall,
		ToolName:              "present",
		Content:               strings.Repeat("display args ", 40),
		ResultContent:         strings.Repeat("result text ", 40),
		Diff:                  strings.Repeat("+diff line\n", 20),
		UserLocalShellCmd:     strings.Repeat("echo shell ", 30),
		UserLocalShellResult:  strings.Repeat("shell output ", 30),
		RawArgs:               strings.Repeat("raw args ", 40),
		ResultPayload:         strings.Repeat("payload ", 40),
		ResultNotes:           []string{strings.Repeat("note ", 20), strings.Repeat("note ", 20)},
		DoneReport:            strings.Repeat("## report\n", 30),
		BackgroundCopyContent: strings.Repeat("background copy ", 20),
		ThinkingParts:         []string{strings.Repeat("thought ", 20)},
		DeliveredFiles: []message.DeliveredFile{{
			ID:          strings.Repeat("id", 20),
			DisplayName: strings.Repeat("name", 20),
			Description: strings.Repeat("desc", 20),
			MIME:        "application/pdf",
			SHA256:      strings.Repeat("a", 64),
		}},
		FileRefs:    []string{strings.Repeat("ref", 20)},
		DoneSummary: strings.Repeat("done summary ", 20),
		ImageParts: []BlockImagePart{{
			FileName:  "shot.png",
			ImagePath: "/tmp/shot.png",
			MimeType:  "image/png",
			Data:      []byte(strings.Repeat("x", 64)),
		}},
		patchPreviewText:     strings.Repeat("*** Update File: sample.go\n+change\n", 20),
		previewRenderedPatch: strings.Repeat("*** Update File: sample.go\n+change\n", 20),
		previewRenderedLines: []string{"+change", "+change"},
		streamingPatchText:   strings.Repeat("+change\n", 20),
		streamingPatchLineStarts: []int{
			0, 8, 16,
		},
	}
}

// TestSpillHotBytesIgnoreResidentOnlyFields pins the hot-bytes contract: the
// estimate may only count what spillBlock actually releases. Growing the
// resident-only fields (UserLocalShellCmd, DoneSummary, FileRefs,
// ImageParts[].Data) must not move the hot estimate, while it must move the
// diagnostic resident estimate — otherwise v.hotBytes would subtract bytes that
// are still held.
func TestSpillHotBytesIgnoreResidentOnlyFields(t *testing.T) {
	base := spillBytesPayloadBlock(1)
	resident := spillBytesPayloadBlock(2)
	resident.UserLocalShellCmd = strings.Repeat("echo shell ", 300)
	resident.DoneSummary = strings.Repeat("done summary ", 300)
	resident.FileRefs = []string{strings.Repeat("ref", 300)}
	resident.ImageParts[0].FileName = strings.Repeat("shot", 300)
	resident.ImageParts[0].ImagePath = strings.Repeat("/tmp/images/", 300)
	resident.ImageParts[0].MimeType = strings.Repeat("image/png", 300)
	resident.ImageParts[0].Data = []byte(strings.Repeat("x", 64*1024))
	resident.PDFNames = []string{strings.Repeat("report", 300)}
	resident.CompactionSummaryRaw = strings.Repeat("preserved context ", 300)
	resident.ThinkingTranslations = []ThinkingTranslationView{{
		TargetLang:   "sample-language",
		Content:      strings.Repeat("translated thought ", 300),
		OriginalHash: strings.Repeat("a", 64),
	}}

	if got, want := resident.estimatedHotBytes(), base.estimatedHotBytes(); got != want {
		t.Fatalf("estimatedHotBytes() with resident-only fields = %d, want %d", got, want)
	}
	if resident.residentBytes() <= base.residentBytes() {
		t.Fatalf("residentBytes() ignored resident-only fields: %d vs %d", resident.residentBytes(), base.residentBytes())
	}
}

// TestSpillHotBytesDropToZeroAfterSpill checks the budget arithmetic: every byte
// counted as hot must be gone once spillBlock returns, so v.hotBytes lands on
// zero and recomputation agrees.
func TestSpillHotBytesDropToZeroAfterSpill(t *testing.T) {
	v := NewViewport(80, 40)
	defer func() { _ = v.spill.Close() }()

	block := spillBytesPayloadBlock(1)
	v.AppendBlock(block)

	hotBefore := v.hotBytes
	if hotBefore <= 0 {
		t.Fatalf("viewport hotBytes = %d, want > 0", hotBefore)
	}
	if got := block.estimatedHotBytes(); got != hotBefore {
		t.Fatalf("block estimatedHotBytes() = %d, viewport hotBytes = %d", got, hotBefore)
	}

	if !v.spillBlock(block) {
		t.Fatal("expected spillBlock to succeed")
	}
	if got := v.hotBytes; got != 0 {
		t.Fatalf("hotBytes after spill = %d, want 0 (every counted byte must be released)", got)
	}
	v.recomputeHotBytes()
	if got := v.hotBytes; got != 0 {
		t.Fatalf("recomputed hotBytes after spill = %d, want 0", got)
	}

	// The fields hot bytes no longer counts must still be resident, otherwise
	// the equality above would pass for the wrong reason.
	if block.UserLocalShellCmd == "" || block.DoneSummary == "" || len(block.FileRefs) == 0 {
		t.Fatal("resident-only fields were cleared; hot bytes must exclude them, not spill them")
	}
	if len(block.ImageParts) != 1 || len(block.ImageParts[0].Data) == 0 || block.ImageParts[0].ImagePath == "" {
		t.Fatal("ImageParts data and metadata must stay resident after spill")
	}
	if block.patchPreviewText != "" || block.previewRenderedPatch != "" || len(block.previewRenderedLines) != 0 ||
		block.streamingPatchText != "" || len(block.streamingPatchLineStarts) != 0 {
		t.Fatal("spill left counted apply_patch caches resident")
	}
}

// TestSpillClearsRecoverablePayloadAndRestores verifies the payload fields that
// became hot are dropped on spill and come back byte-identical from the spill
// store, which is what makes dropping them safe.
func TestSpillClearsRecoverablePayloadAndRestores(t *testing.T) {
	v := NewViewport(80, 40)
	defer func() { _ = v.spill.Close() }()

	wantRawArgs := `{"report":"` + strings.Repeat("full args ", 40) + `"}`
	wantDoneReport := strings.Repeat("## Completion\n- shipped\n", 20)
	wantCopy := strings.Repeat("bare background ", 20)
	wantNotes := []string{"Sample diagnostic note", "another note"}
	wantFiles := []message.DeliveredFile{
		{ID: "file-1", DisplayName: "summary.pdf", Description: "report", MIME: "application/pdf", SHA256: strings.Repeat("b", 64)},
		{ID: "file-2", DisplayName: "notes.md", MIME: "text/markdown"},
	}
	block := &Block{
		ID:                    2,
		Type:                  BlockToolCall,
		ToolName:              "done",
		Content:               `{"report":"short"}`,
		ResultContent:         "result body\nSample diagnostic note",
		RawArgs:               wantRawArgs,
		ResultPayload:         "result body",
		ResultNotes:           append([]string(nil), wantNotes...),
		DoneReport:            wantDoneReport,
		BackgroundCopyContent: wantCopy,
		DeliveredFiles:        wantFiles,
		ResultDone:            true,
		ResultStatus:          message.ToolStatusSuccess,
	}
	v.AppendBlock(block)

	if !v.spillBlock(block) {
		t.Fatal("expected spillBlock to succeed")
	}
	if block.RawArgs != "" || block.ResultPayload != "" || block.DoneReport != "" {
		t.Fatalf("spill left payload behind: raw=%q payload=%q report=%q", block.RawArgs, block.ResultPayload, block.DoneReport)
	}
	if len(block.ResultNotes) != 0 || block.BackgroundCopyContent != "" {
		t.Fatalf("spill left notes/copy content behind: notes=%d copy=%q",
			len(block.ResultNotes), block.BackgroundCopyContent)
	}
	// DeliveredFiles is an activation target (Enter opens the files on a cold
	// block without materializing it), so it stays resident by design.
	if len(block.DeliveredFiles) != len(wantFiles) {
		t.Fatalf("spill cleared DeliveredFiles: %d, want %d", len(block.DeliveredFiles), len(wantFiles))
	}

	if err := block.ensureMaterialized(); err != nil {
		t.Fatalf("ensureMaterialized(): %v", err)
	}
	if block.spillCold {
		t.Fatal("expected block to hydrate")
	}
	if block.RawArgs != wantRawArgs {
		t.Fatalf("restored RawArgs = %q, want %q", block.RawArgs, wantRawArgs)
	}
	if block.ResultPayload != "result body" {
		t.Fatalf("restored ResultPayload = %q", block.ResultPayload)
	}
	if !reflect.DeepEqual(block.ResultNotes, wantNotes) {
		t.Fatalf("restored ResultNotes = %#v", block.ResultNotes)
	}
	if block.DoneReport != wantDoneReport {
		t.Fatalf("restored DoneReport mismatch (%d bytes)", len(block.DoneReport))
	}
	if block.BackgroundCopyContent != wantCopy {
		t.Fatalf("restored BackgroundCopyContent mismatch (%d bytes)", len(block.BackgroundCopyContent))
	}
	if !reflect.DeepEqual(block.DeliveredFiles, wantFiles) {
		t.Fatalf("restored DeliveredFiles = %#v", block.DeliveredFiles)
	}
	if block.estimatedHotBytes() <= 0 {
		t.Fatal("hydrated block should report hot bytes again")
	}
}

// TestResidentBytesSupersetOfHotBytes keeps the diagnostic estimate from
// drifting below the budget input: every block must report at least as many
// resident bytes as hot bytes, hot or cold.
func TestResidentBytesSupersetOfHotBytes(t *testing.T) {
	blocks := []*Block{
		{
			ID:                1,
			Type:              BlockUser,
			Content:           strings.Repeat("user text ", 50),
			UserLocalShellCmd: "ls -la",
			FileRefs:          []string{"a.go", "b.go"},
			ImageParts:        []BlockImagePart{{FileName: "a.png", Data: []byte(strings.Repeat("i", 2048))}},
		},
		{
			ID:                    2,
			Type:                  BlockToolCall,
			ToolName:              "present",
			Content:               strings.Repeat("args ", 40),
			RawArgs:               strings.Repeat("raw ", 40),
			ResultContent:         strings.Repeat("result ", 40),
			ResultPayload:         strings.Repeat("payload ", 20),
			ResultNotes:           []string{"note"},
			DoneReport:            strings.Repeat("report ", 30),
			DoneSummary:           strings.Repeat("summary ", 10),
			BackgroundCopyContent: strings.Repeat("copy ", 10),
			DeliveredFiles:        []message.DeliveredFile{{ID: "f", DisplayName: "f.txt", MIME: "text/plain"}},
		},
		{
			ID:                   3,
			Type:                 BlockAssistant,
			Content:              strings.Repeat("assistant ", 60),
			ThinkingParts:        []string{strings.Repeat("think ", 20)},
			patchPreviewText:     strings.Repeat("+patch\n", 20),
			previewRenderedPatch: strings.Repeat("+patch\n", 20),
			previewRenderedLines: []string{"+patch", "+patch"},
		},
	}

	for i, block := range blocks {
		if got, want := block.residentBytes(), block.estimatedHotBytes(); got < want {
			t.Fatalf("block %d: residentBytes() = %d < estimatedHotBytes() = %d", i, got, want)
		}
	}

	v := NewViewport(80, 40)
	defer func() { _ = v.spill.Close() }()
	v.AppendBlocks(blocks)

	var hotTotal int64
	for _, block := range v.blocks {
		hotTotal += block.estimatedHotBytes()
	}
	if got := v.ResidentBytes(); got < hotTotal {
		t.Fatalf("ResidentBytes() = %d < sum(estimatedHotBytes) = %d", got, hotTotal)
	}

	for _, block := range v.blocks {
		if !v.spillBlock(block) {
			t.Fatalf("spillBlock(%d) failed", block.ID)
		}
	}
	hotTotal = 0
	for _, block := range v.blocks {
		hotTotal += block.estimatedHotBytes()
		if got, want := block.residentBytes(), block.estimatedHotBytes(); got < want {
			t.Fatalf("cold block %d: residentBytes() = %d < estimatedHotBytes() = %d", block.ID, got, want)
		}
	}
	if got := v.ResidentBytes(); got < hotTotal {
		t.Fatalf("cold ResidentBytes() = %d < sum(estimatedHotBytes) = %d", got, hotTotal)
	}
}
