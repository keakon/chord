package requesttrim

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func imageEvictionContext(age int, parts []message.ContentPart, recoverable bool) Context {
	return Context{
		ToolName:         tools.NameViewImage,
		Content:          "Loaded image into context.",
		Age:              age,
		Policy:           DefaultPolicy(),
		Parts:            parts,
		ImageRecoverable: recoverable,
	}
}

func imageEvictionParts(paths ...string) []message.ContentPart {
	parts := []message.ContentPart{{Type: message.ContentPartText, Text: "Loaded image into context."}}
	for _, path := range paths {
		parts = append(parts, message.ContentPart{Type: message.ContentPartImage, MimeType: "image/png", ImagePath: path, DataBytes: 4096})
	}
	return parts
}

func TestClassifyEvictsImagePartsByStaleAge(t *testing.T) {
	parts := imageEvictionParts("/tmp/session/images/shot.png")
	tests := []struct {
		name  string
		age   int
		parts []message.ContentPart
		want  bool
	}{
		{name: "stale age evicts", age: DefaultPolicy().StaleAgeTurns, parts: parts, want: true},
		{name: "recent age keeps", age: DefaultPolicy().StaleAgeTurns - 1, parts: parts, want: false},
		{name: "pdf-only parts never evict", age: DefaultPolicy().StaleAgeTurns + 10, parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "doc"},
			{Type: message.ContentPartPDF, MimeType: "application/pdf", DataBytes: 4096},
		}, want: false},
		{name: "no parts never evict", age: DefaultPolicy().StaleAgeTurns + 10, parts: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict := Classify(imageEvictionContext(tt.age, tt.parts, false))
			if verdict.EvictImages != tt.want {
				t.Fatalf("EvictImages = %v, want %v", verdict.EvictImages, tt.want)
			}
			// Image eviction rides alongside the string classification without
			// changing it: the content here is short, so no string rule claims it.
			if verdict.Class != None {
				t.Fatalf("Class = %q, want none for short content", verdict.Class)
			}
		})
	}
}

func TestRenderImageEvictionRecoveryBranches(t *testing.T) {
	tests := []struct {
		name        string
		parts       []message.ContentPart
		recoverable bool
		wantPath    string
		wantPaths   []string
		wantReload  bool // "not re-loadable" wording expected
	}{
		{
			name:        "recoverable single image carries the address",
			parts:       imageEvictionParts("/tmp/session/images/shot.png"),
			recoverable: true,
			wantPath:    "/tmp/session/images/shot.png",
		},
		{
			name:        "unrecoverable capability says so",
			parts:       imageEvictionParts("/tmp/session/images/shot.png"),
			recoverable: false,
			wantReload:  true,
		},
		{
			name:        "transient blob without a path has no address",
			parts:       imageEvictionParts(""),
			recoverable: true,
			wantReload:  true,
		},
		{
			name:        "multiple images use plural wording",
			parts:       imageEvictionParts("/tmp/session/images/a.png", "/tmp/session/images/b.png"),
			recoverable: true,
			wantPath:    "/tmp/session/images/a.png",
			wantPaths:   []string{"/tmp/session/images/b.png"},
		},
		{
			name:        "mixed paths report the missing image",
			parts:       imageEvictionParts("/tmp/session/images/a.png", ""),
			recoverable: true,
			wantPath:    "/tmp/session/images/a.png",
			wantPaths:   []string{"Image 2 is not re-loadable in this session", "re-run the tool"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marker, ok := RenderImageEviction(imageEvictionContext(DefaultPolicy().StaleAgeTurns, tt.parts, tt.recoverable))
			if !ok {
				t.Fatal("RenderImageEviction reported no image parts")
			}
			if !strings.HasPrefix(marker, "[Older image output") || !strings.HasSuffix(marker, "]") {
				t.Fatalf("marker not bracketed in the reduction style: %q", marker)
			}
			if tt.wantPath != "" {
				if !strings.Contains(marker, tt.wantPath) {
					t.Fatalf("marker missing recovery address %q: %q", tt.wantPath, marker)
				}
				if !strings.Contains(marker, "view_image") {
					t.Fatalf("marker missing view_image recall route: %q", marker)
				}
				if imagesRecoverable(imageEvictionContext(DefaultPolicy().StaleAgeTurns, tt.parts, tt.recoverable)) && strings.Contains(marker, "not re-loadable") {
					t.Fatalf("recoverable marker must not claim irrecoverability: %q", marker)
				}
			}
			for _, want := range tt.wantPaths {
				if !strings.Contains(marker, want) {
					t.Fatalf("marker missing %q: %q", want, marker)
				}
			}
			if tt.wantReload {
				if !strings.Contains(marker, "not re-loadable in this session") {
					t.Fatalf("marker must state the image cannot be re-loaded: %q", marker)
				}
				if !strings.Contains(marker, "re-run the tool") {
					t.Fatalf("marker must point at re-running the producing tool: %q", marker)
				}
				if strings.Contains(marker, "saved at") {
					t.Fatalf("unrecoverable marker must not carry an address: %q", marker)
				}
			}
			wantPlural := countImageParts(tt.parts) > 1
			if strings.Contains(marker, "image outputs") != wantPlural {
				t.Fatalf("plural wording mismatch for %d images: %q", countImageParts(tt.parts), marker)
			}
		})
	}
}

func TestRenderImageEvictionWithoutImageParts(t *testing.T) {
	if _, ok := RenderImageEviction(imageEvictionContext(DefaultPolicy().StaleAgeTurns, imageEvictionParts("/p.png"), true)); !ok {
		t.Fatal("image parts must render")
	}
	textOnly := []message.ContentPart{{Type: message.ContentPartText, Text: "text only"}}
	if _, ok := RenderImageEviction(imageEvictionContext(DefaultPolicy().StaleAgeTurns, textOnly, true)); ok {
		t.Fatal("parts without images must not render an eviction marker")
	}
}

func TestRetentionDecisionImageEvictionRecovery(t *testing.T) {
	parts := imageEvictionParts("/tmp/session/images/shot.png")
	verdict := Classify(imageEvictionContext(DefaultPolicy().StaleAgeTurns, parts, true))
	decision := RetentionDecision(imageEvictionContext(DefaultPolicy().StaleAgeTurns, parts, true), verdict, "", "")
	if decision.Level != retentionReference {
		t.Fatalf("eviction-only level = %q, want reference", decision.Level)
	}
	if decision.Recovery != retentionRecoveryRereadFile || !decision.Recoverable() {
		t.Fatalf("recoverable eviction decision = %+v", decision)
	}

	unrecoverable := Classify(imageEvictionContext(DefaultPolicy().StaleAgeTurns, parts, false))
	decision = RetentionDecision(imageEvictionContext(DefaultPolicy().StaleAgeTurns, parts, false), unrecoverable, "", "")
	if decision.Recovery != retentionRecoveryUnavailable || decision.Recoverable() {
		t.Fatalf("unrecoverable eviction decision = %+v", decision)
	}

	// A string reduction alongside the eviction adopts the image route when it
	// exists, and reports the loss when it does not.
	bigContent := strings.Repeat("stale payload line\n", 200)
	stringCtx := Context{
		ToolName:         "custom_tool",
		Content:          bigContent,
		Age:              DefaultPolicy().StaleAgeTurns,
		Policy:           DefaultPolicy(),
		Parts:            parts,
		ImageRecoverable: false,
		ToolResults:      DefaultPolicy().MinToolResultsPrune,
	}
	stringVerdict := Classify(stringCtx)
	if stringVerdict.Class == None || !stringVerdict.EvictImages {
		t.Fatalf("combined verdict = %+v, want a string class plus eviction", stringVerdict)
	}
	combined := RetentionDecision(stringCtx, stringVerdict, string(stringVerdict.Class), "marker")
	if combined.Recovery != retentionRecoveryUnavailable {
		t.Fatalf("combined decision recovery = %q, want unavailable while the image has no route", combined.Recovery)
	}
	stringCtx.ImageRecoverable = true
	combined = RetentionDecision(stringCtx, stringVerdict, string(stringVerdict.Class), "marker")
	if combined.Recovery != retentionRecoveryRereadFile {
		t.Fatalf("combined decision recovery = %q, want the image re-load route when available", combined.Recovery)
	}
	stringCtx.Parts = imageEvictionParts("/tmp/session/images/a.png", "")
	combined = RetentionDecision(stringCtx, stringVerdict, string(stringVerdict.Class), "marker")
	if combined.Recovery != retentionRecoveryUnavailable {
		t.Fatalf("partial image recovery must not report the whole output recoverable: %+v", combined)
	}
}

func TestImageReviewAgeStaysPendingUntilEviction(t *testing.T) {
	ctx := imageEvictionContext(DefaultPolicy().StaleAgeTurns+10, imageEvictionParts("/tmp/session/images/shot.png"), true)
	if age := NextReviewAge(ctx); age != ctx.Policy.StaleAgeTurns {
		t.Fatalf("pending image review age=%d, want %d", age, ctx.Policy.StaleAgeTurns)
	}
	ctx.Parts = nil
	if age := NextImageReviewAge(ctx); age != 0 {
		t.Fatalf("evicted images must not keep an image frontier: %d", age)
	}
}

func TestImagePayloadBytesCountsOnlyImages(t *testing.T) {
	parts := []message.ContentPart{
		{Type: message.ContentPartText, Text: "text"},
		{Type: message.ContentPartImage, ImagePath: "/a.png", DataBytes: 100},
		{Type: message.ContentPartPDF, ImagePath: "/b.pdf", DataBytes: 500},
		{Type: message.ContentPartImage, Data: make([]byte, 300)},
	}
	if got := ImagePayloadBytes(parts); got != 400 {
		t.Fatalf("ImagePayloadBytes = %d, want 400", got)
	}
	if got := ImagePayloadBytes(nil); got != 0 {
		t.Fatalf("ImagePayloadBytes(nil) = %d, want 0", got)
	}
}
