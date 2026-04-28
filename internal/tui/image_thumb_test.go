package tui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func makeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{R: 255, G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestRenderUserPlainImageRenderRangeIncludesCardPadding(t *testing.T) {
	ApplyTheme(DefaultTheme())
	setCurrentTerminalImageCapabilities(TerminalImageCapabilities{Backend: ImageBackendNone})
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         1,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{
			FileName:        "sample.png",
			MimeType:        "image/png",
			Data:            pngData,
			RenderStartLine: -1,
			RenderEndLine:   -1,
		}},
	}

	const renderWidth = 40
	lines := block.Render(renderWidth, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered lines")
	}

	style := UserCardStyle
	boxWidth := renderWidth - style.GetHorizontalMargins()
	innerWidth := boxWidth - style.GetHorizontalPadding() - style.GetHorizontalBorderSize()
	contentWidth := innerWidth - 2
	imageLines, _, _, err := renderImageBlock(block.ImageParts[0], contentWidth, currentTheme.UserCardBg, currentImageCapabilities())
	if err != nil {
		t.Fatalf("renderImageBlock: %v", err)
	}

	expectedStart := style.GetPaddingTop() + 2 // USER label + spacer line
	expectedEnd := expectedStart + len(imageLines) - 1
	if got := block.ImageParts[0].RenderStartLine; got != expectedStart {
		t.Fatalf("RenderStartLine = %d, want %d", got, expectedStart)
	}
	if got := block.ImageParts[0].RenderEndLine; got != expectedEnd {
		t.Fatalf("RenderEndLine = %d, want %d", got, expectedEnd)
	}
	if _, ok := block.imagePartAtLine(expectedStart-1, renderWidth); ok {
		t.Fatal("unexpected image hit on card padding line")
	}
	if _, ok := block.imagePartAtLine(expectedStart, renderWidth); !ok {
		t.Fatal("expected image hit on render body line")
	}
	if _, ok := block.imagePartAtLine(expectedEnd, renderWidth); !ok {
		t.Fatal("expected fallback image hit on label-only line")
	}
}

func TestImagePartAtLineExcludesLabelRowWhenInlineBodyExists(t *testing.T) {
	part := BlockImagePart{RenderStartLine: 4, RenderRows: 3, RenderEndLine: 7}
	if !imagePartLineHit(part, 4) {
		t.Fatal("expected first body row to hit")
	}
	if !imagePartLineHit(part, 6) {
		t.Fatal("expected last body row to hit")
	}
	if imagePartLineHit(part, 7) {
		t.Fatal("label row should not hit when inline body exists")
	}
}

func TestImagePartAtPointRequiresHorizontalHitWithinImageBody(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type: BlockUser,
		ImageParts: []BlockImagePart{{
			FileName:        "sample.png",
			RenderStartLine: 3,
			RenderEndLine:   5,
			RenderCols:      12,
			RenderRows:      2,
		}},
	}
	const width = 80
	bodyLeft := imagePartBodyLeftColumn()
	if _, ok := block.imagePartAtPoint(3, bodyLeft-1, width); ok {
		t.Fatal("click left of image body should not hit")
	}
	if _, ok := block.imagePartAtPoint(3, bodyLeft, width); !ok {
		t.Fatal("click on first image column should hit")
	}
	if _, ok := block.imagePartAtPoint(4, bodyLeft+11, width); !ok {
		t.Fatal("click on last image column should hit")
	}
	if _, ok := block.imagePartAtPoint(4, bodyLeft+12, width); ok {
		t.Fatal("click right of image body should not hit")
	}
	if _, ok := block.imagePartAtPoint(5, bodyLeft, width); ok {
		t.Fatal("label row should not hit")
	}
}

func TestBlockPlainContentIncludesImagePlaceholderLabels(t *testing.T) {
	block := &Block{
		Type:       BlockUser,
		Content:    "describe this",
		ImageCount: 1,
		ImageParts: []BlockImagePart{{
			FileName: "sample.png",
			MimeType: "image/png",
			Data:     []byte{1, 2, 3},
		}},
	}

	got := blockPlainContent(block)
	if !strings.Contains(got, "describe this") {
		t.Fatalf("blockPlainContent = %q, want original text", got)
	}
	if !strings.Contains(got, "[image: sample.png]") {
		t.Fatalf("blockPlainContent = %q, want image placeholder label", got)
	}
}

func TestBlockPlainContentSkillToolIncludesNamePathAndBody(t *testing.T) {
	block := &Block{
		Type:          BlockToolCall,
		ToolName:      "Skill",
		Content:       `{"name":"skill-creator","result":"<path>/tmp/skills/skill-creator/SKILL.md</path>"}`,
		ResultContent: "<skill>\n<name>skill-creator</name>\n<path>/tmp/skills/skill-creator/SKILL.md</path>\n<root>/tmp/skills/skill-creator</root>\n\n# Skill Creator\n\n- Step one\n</skill>",
	}

	got := blockPlainContent(block)
	for _, want := range []string{"Name: skill-creator", "Path: /tmp/skills/skill-creator/SKILL.md", "# Skill Creator", "Step one"} {
		if !strings.Contains(got, want) {
			t.Fatalf("blockPlainContent = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "<skill>") || strings.Contains(got, "<root>") {
		t.Fatalf("blockPlainContent should omit wrapper tags, got %q", got)
	}
}

func TestBlockPlainContentAssistantCodeBlockUnaffectedByDisplayWrapIndent(t *testing.T) {
	block := &Block{
		Type:    BlockAssistant,
		Content: "```go\nfunc TestCancelCurrentTurnRoutesToFocusedSubAgentAndPersistsCancelledToolResult(t *testing.T) {}\n```",
	}
	got := blockPlainContent(block)
	if got != strings.TrimSpace(block.Content) {
		t.Fatalf("blockPlainContent = %q, want original content %q", got, strings.TrimSpace(block.Content))
	}
}

func TestImagePartDisplayNameNormalizesClipboardName(t *testing.T) {
	if got := imagePartDisplayName("clipboard.png", "", "image/png", 2); got != "image2.png" {
		t.Fatalf("imagePartDisplayName() = %q, want %q", got, "image2.png")
	}
	if got := imagePartDisplayName("", "clipboard.png", "image/png", 3); got != "image3.png" {
		t.Fatalf("imagePartDisplayName() from path = %q, want %q", got, "image3.png")
	}
}

func TestImagePartDisplayNameKeepsRegularFileName(t *testing.T) {
	if got := imagePartDisplayName("diagram.jpg", "", "image/jpeg", 1); got != "diagram.jpg" {
		t.Fatalf("imagePartDisplayName() = %q, want %q", got, "diagram.jpg")
	}
}

func TestSelectionImagePlaceholderReturnsImageLabel(t *testing.T) {
	block := &Block{
		Type: BlockUser,
		ImageParts: []BlockImagePart{{
			FileName:        "sample.png",
			RenderStartLine: 3,
			RenderEndLine:   5,
		}},
	}
	if got := selectionImagePlaceholder(block, 4); got != "[image: sample.png]" {
		t.Fatalf("selectionImagePlaceholder = %q, want %q", got, "[image: sample.png]")
	}
	if got := selectionImagePlaceholder(block, 2); got != "" {
		t.Fatalf("selectionImagePlaceholder outside range = %q, want empty", got)
	}
}
