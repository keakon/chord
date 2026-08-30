package tools

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestNormalizePunctWithSpaceFoldingIgnoresInvisibleRunes guards that
// variation selectors (U+FE0F) and zero-width spaces (U+200B) are
// folded out of the comparison sequence (no normalized rune), while
// each is merged into the adjacent span so splicing a match back into the
// file keeps the file's original bytes.

func TestNormalizePunctWithSpaceFoldingIgnoresInvisibleRunes(t *testing.T) {
	norm, spans := normalizePunctWithSpaceFolding([]rune("ab\uFE0Fc\u200Bd"))
	if got := string(norm); got != "abcd" {
		t.Fatalf("norm = %q, want %q (invisible runes folded out)", got, "abcd")
	}
	// b's span absorbs the following VS16; c's absorbs the ZWSP; the
	// original file bytes for both are preserved by the widened spans.
	want := []punctSpan{
		{start: 0, end: 1},
		{start: 1, end: 3},
		{start: 3, end: 5},
		{start: 5, end: 6},
	}
	if len(spans) != len(want) {
		t.Fatalf("len(spans) = %d, want %d", len(spans), len(want))
	}
	for i, got := range spans {
		if got != want[i] {
			t.Fatalf("spans[%d] = %+v, want %+v", i, got, want[i])
		}
	}
}

// TestNormalizePunctWithSpaceFoldingIgnoresLeadingInvisibleRune guards
// the boundary case where an invisible rune leads the text: no previous span
// exists to absorb it, so it is simply dropped from the comparison (and stays
// outside any span, preserved by the untouched prefix when splicing).

func TestNormalizePunctWithSpaceFoldingIgnoresLeadingInvisibleRune(t *testing.T) {
	norm, spans := normalizePunctWithSpaceFolding([]rune("\u200Bab"))
	if got := string(norm); got != "ab" {
		t.Fatalf("norm = %q, want %q (leading ZWSP folded out)", got, "ab")
	}
	if len(spans) != 2 || spans[0] != (punctSpan{start: 1, end: 2}) {
		t.Fatalf("spans = %+v, want [{1,2} {2,3}]", spans)
	}
}

// TestEditToolTolerantMatchIgnoresInvisibleRunes guards the full Edit flow:
// a model copy of the target that leaks a VS16 plus a full-width comma still
// replaces the file's half-width-comma text through the tolerant fallback.

func TestEditToolTolerantMatchIgnoresInvisibleRunes(t *testing.T) {
	dir := t.TempDir()
	file := "ab,cd\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "ab\uFE0F\uFF0C cd\n"

	newText := "ab;cd\n"
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want tolerant success", err)
	}
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "ab;cd\n" {
		t.Fatalf("file = %q, want %q (VS16 ignored during match)", string(got), "ab;cd\n")
	}
}

// TestBandedEditDistanceMatchesExact guards bandedEditDistance against the
// exact full-matrix levenshteinDistance: within the band the two must agree,
// and beyond it the banded form must report ok=false rather than a capped
// approximation. Exhaustive over short rune strings keeps the bound honest.
func TestBandedEditDistanceMatchesExact(t *testing.T) {
	alphabet := []string{"a", "b", "c"}
	var strs []string
	strs = append(strs, "")
	strs = append(strs, alphabet...)
	for _, a := range alphabet {
		for _, b := range alphabet {
			strs = append(strs, a+b)
		}
	}
	for _, a := range alphabet {
		for _, b := range alphabet {
			for _, c := range alphabet {
				strs = append(strs, a+b+c)
			}
		}
	}
	for _, x := range strs {
		for _, y := range strs {
			exact := levenshteinDistance(x, y)
			for maxDist := 0; maxDist <= 3; maxDist++ {
				got, ok := bandedEditDistance(x, y, maxDist)
				if exact <= maxDist {
					if !ok || got != exact {
						t.Fatalf("bandedEditDistance(%q, %q, %d) = (%d, %v), want (%d, true)", x, y, maxDist, got, ok, exact)
					}
				} else if ok {
					t.Fatalf("bandedEditDistance(%q, %q, %d) = (%d, true), want false (exact %d)", x, y, maxDist, got, exact)
				}
			}
		}
	}
}

// TestEditClosestMatchDeepWindow guards the seed-and-verify rewrite: a block
// whose real match sits deep in the file must still be located. The old
// per-window scan consumed its rune-pair budget near the top of the file, so
// a deep target fell through to the generic re-read hint; seed lookup is
// independent of depth. The padding uses the same shape as the sought block
// (uniform lines, equal length) so the old scan's length pre-filter cannot
// skip the windows for free: every window is fully processed and the budget
// runs out long before the deep match is reached, while the seed still pins
// the exact block.
func TestEditClosestMatchDeepWindow(t *testing.T) {
	const line = "padding line %04d with some filler text to widen each row"
	var b strings.Builder
	for i := range 1200 {
		fmt.Fprintf(&b, line+"\n", i)
	}
	content := b.String()
	old := fmt.Sprintf(line+"\n", 500) + fmt.Sprintf(line+"x\n", 501)
	cl, ok := editClosestMatch(content, old)
	if !ok {
		t.Fatal("editClosestMatch = ok=false, want deep window located")
	}
	if cl.StartLine != 501 {
		t.Fatalf("StartLine = %d, want 501", cl.StartLine)
	}
	if cl.Similarity <= 0.9 || cl.DiffRunes != 1 {
		t.Fatalf("closest = line %d sim %.3f diff %d, want line 501 sim>0.9 diff 1", cl.StartLine, cl.Similarity, cl.DiffRunes)
	}
}

// TestEditClosestMatchLargeOldBlock guards blocks large enough that a single
// full-block Levenshtein would dwarf the old scan budget: seed lookup must
// still find the window instead of degrading to the generic hint.
func TestEditClosestMatchLargeOldBlock(t *testing.T) {
	var b strings.Builder
	for i := range 40 {
		fmt.Fprintf(&b, "func f%d(x int) int {\n\treturn x + %d\n}\n\n", i, i)
	}
	content := b.String()
	var o strings.Builder
	for i := 20; i < 32; i++ {
		fmt.Fprintf(&o, "func f%d(x int) int {\n\treturn x + %d\n}\n\n", i, i)
	}
	// Introduce one transcription error in the last line of the block.
	old := strings.TrimSuffix(o.String(), "\n\n") + "\n\treturn x + 31x\n}\n"
	cl, ok := editClosestMatch(content, old)
	if !ok {
		t.Fatal("editClosestMatch = ok=false, want large-block window located")
	}
	if cl.StartLine != 20*4+1 {
		t.Fatalf("StartLine = %d, want %d", cl.StartLine, 20*4+1)
	}
}

// TestEditClosestMatchCommonSeedLine guards seed choice: when the block
// contains a unique line, that line must be picked as the seed over a far
// more common one (the blank line), so verification visits a single window
// instead of the capped candidate flood.
func TestEditClosestMatchCommonSeedLine(t *testing.T) {
	var b strings.Builder
	for range 20 {
		b.WriteString("var v int\n\n")
	}
	b.WriteString("var w int\nvar v int\n")
	content := b.String()
	old := "\nvar w int\nvar v int\n"
	cl, ok := editClosestMatch(content, old)
	if !ok {
		t.Fatal("editClosestMatch = ok=false, want window located")
	}
	if cl.StartLine != 40 {
		t.Fatalf("StartLine = %d, want 40", cl.StartLine)
	}
	if cl.DiffRunes != 0 || cl.Similarity != 1.0 {
		t.Fatalf("closest = line %d sim %.3f diff %d, want identical match", cl.StartLine, cl.Similarity, cl.DiffRunes)
	}
}

// TestEditClosestMatchBlankLineSeed guards the seed sentinel: a blank line
// normalizes to "", so the seed string cannot double as the "not chosen yet"
// marker — the blank line is a legitimate seed and must survive as one. When
// it is the block's only line present in the file, dropping it loses the
// hint entirely.
func TestEditClosestMatchBlankLineSeed(t *testing.T) {
	var b strings.Builder
	for i := range 300 {
		fmt.Fprintf(&b, "file line %04d %s\n", i, strings.Repeat("z", 300))
		b.WriteString("\n")
	}
	// The block's only line present in the file is the blank one; the long
	// line carries a single transcription error. oldRunes > 256, so the
	// full-window fallback does not apply and the seed is all there is.
	old := "\nfile line 0150 " + strings.Repeat("z", 299) + "Q"
	cl, ok := editClosestMatch(b.String(), old)
	if !ok {
		t.Fatal("editClosestMatch = ok=false, want the blank-line seed to locate the window")
	}
	if cl.StartLine != 300 {
		t.Fatalf("StartLine = %d, want 300", cl.StartLine)
	}
	if cl.DiffRunes != 1 || cl.Similarity < 0.99 {
		t.Fatalf("closest = line %d sim %.4f diff %d, want line 300 diff 1", cl.StartLine, cl.Similarity, cl.DiffRunes)
	}
}

// TestEditClosestMatchDeepCommonSeed guards candidate ordering: when every
// line of the block is boilerplate, the seed has thousands of hits and the
// verification cap decides which windows are reachable. Verifying them in
// file order would re-create the depth bias the seed lookup removes — the
// deep window here is strictly more similar (diff 1) than anything near the
// top (diff 3) and must win despite sitting 4500 lines down.
func TestEditClosestMatchDeepCommonSeed(t *testing.T) {
	var b strings.Builder
	for i := range 2000 {
		b.WriteString("func gen() {\n")
		if i == 1500 {
			b.WriteString("\treturn xGOOD\n")
		} else {
			b.WriteString("\treturn x\n")
		}
		b.WriteString("}\n")
	}
	// "return xGOD" (dropped an O) matches no line in the file, so the only
	// seeds are "func gen() {" and "}" — 2000 hits each.
	cl, ok := editClosestMatch(b.String(), "func gen() {\n\treturn xGOD\n}\n")
	if !ok {
		t.Fatal("editClosestMatch = ok=false, want the deep window located")
	}
	if want := 1500*3 + 1; cl.StartLine != want {
		t.Fatalf("StartLine = %d, want %d (candidates must be ranked by alignment, not file order)", cl.StartLine, want)
	}
	if cl.DiffRunes != 1 {
		t.Fatalf("DiffRunes = %d, want 1", cl.DiffRunes)
	}
}

func TestQuoteToolLineEscapesVariationSelectors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "a b", `"a b"`},
		{"tab escapes", "a\tb", `"a\tb"`},
		{"quote escapes", `a"b`, `"a\"b"`},
		{"orphan FE0F visible", "\ufe0f0", `"\ufe0f0"`},
		{"orphan FE0E visible", "x\ufe0ey", `"x\ufe0ey"`},
		{"literal backslash-u text untouched", `\ufe0f`, `"\\ufe0f"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteToolLine(tc.in); got != tc.want {
				t.Fatalf("quoteToolLine(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestFirstRuneDiffLoc(t *testing.T) {
	tests := []struct {
		name           string
		a, b           string
		wantOff        int
		wantER, wantAR rune
		wantEP, wantAP bool
	}{
		{"identical", "abc", "abc", 0, 0, 0, false, false},
		{"middle rune", "abc", "axc", 1, 'b', 'x', true, true},
		{"expected shorter", "ab", "abc", 2, 0, 'c', false, true},
		{"actual shorter", "abc", "ab", 2, 'c', 0, true, false},
		{"invisible selector first", "\ufe0f0", "0", 0, '\ufe0f', '0', true, true},
		{"both empty", "", "", 0, 0, 0, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			off, er, ar, ep, ap := firstRuneDiffLoc(tc.a, tc.b)
			if off != tc.wantOff || er != tc.wantER || ar != tc.wantAR || ep != tc.wantEP || ap != tc.wantAP {
				t.Fatalf("firstRuneDiffLoc(%q, %q) = (%d, %U, %U, %v, %v), want (%d, %U, %U, %v, %v)",
					tc.a, tc.b, off, er, ar, ep, ap, tc.wantOff, tc.wantER, tc.wantAR, tc.wantEP, tc.wantAP)
			}
		})
	}
}

func TestToolRuneToken(t *testing.T) {
	if got := toolRuneToken('\ufe0f', true); got != "U+FE0F" {
		t.Fatalf("toolRuneToken = %q, want U+FE0F", got)
	}
	if got := toolRuneToken(0, false); got != "<absent>" {
		t.Fatalf("toolRuneToken absent = %q, want <absent>", got)
	}
}

func TestAlignEditWindowLines(t *testing.T) {
	norm := normalizePatchPunctuationLine
	tests := []struct {
		name             string
		old, src         []string
		wantOld, wantSrc int
		wantBlankOnly    bool
	}{
		{"identical", []string{"a", "b"}, []string{"a", "b"}, 0, 0, false},
		{"replacement stays paired", []string{"a", "b", "c"}, []string{"a", "", "b"}, 1, 1, false},
		{"blank shift", []string{"a", "", "b"}, []string{"a", "b", ""}, 1, 1, true},
		{"old has one extra blank", []string{"a", "", "", "b"}, []string{"a", "", "b", ""}, 1, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldExtra, srcExtra, blankOnly := alignEditWindowLines(tc.old, tc.src, 0, norm)
			if oldExtra != tc.wantOld || srcExtra != tc.wantSrc || blankOnly != tc.wantBlankOnly {
				t.Fatalf("alignEditWindowLines(%q, %q) = (%d, %d, %v), want (%d, %d, %v)",
					tc.old, tc.src, oldExtra, srcExtra, blankOnly, tc.wantOld, tc.wantSrc, tc.wantBlankOnly)
			}
		})
	}
}
