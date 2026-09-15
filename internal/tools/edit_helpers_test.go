package tools

import (
	"errors"
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

// TestNormalizePunctWithSpaceFoldingFoldsOrphanCombiningMark guards the
// tokenizer-artifact path: a combining mark (U+0304) at the start of the
// string or after only whitespace is folded out, while the same mark over a
// visible base (letter, digit, symbol, punctuation) is preserved as
// potentially legitimate content.

func TestNormalizePunctWithSpaceFoldingFoldsOrphanCombiningMark(t *testing.T) {
	// U+0304 after a space (the "### ̄.2.1" shape) is orphaned and folded.
	norm, spans := normalizePunctWithSpaceFolding([]rune("### \u0304.2.1"))
	if got := string(norm); got != "### .2.1" {
		t.Fatalf("norm = %q, want %q (orphan U+0304 folded)", got, "### .2.1")
	}
	// The orphan mark merges into the preceding visible rune's span so
	// splice-back keeps the file's bytes — the space at raw index 3
	// absorbs it (span {3 5} covers ' ' + U+0304); normalized index 3 is
	// that space in "### .2.1".
	if len(spans) < 4 || spans[3] != (punctSpan{start: 3, end: 5}) {
		t.Fatalf("space span = %+v, want {3 5} (' ' absorbs the orphan mark)", spans)
	}

	// Same mark after a letter is preserved (Arabic/Devanagari/Vietnamese).
	norm2, _ := normalizePunctWithSpaceFolding([]rune("a\u0304b"))
	if got := string(norm2); got != "a\u0304b" {
		t.Fatalf("norm = %q, want %q (letter-combining mark preserved)", got, "a\u0304b")
	}

	// Orphan mark at the very start is folded (no preceding rune at all).
	norm3, _ := normalizePunctWithSpaceFolding([]rune("\u0304ab"))
	if got := string(norm3); got != "ab" {
		t.Fatalf("norm = %q, want %q (leading orphan mark folded)", got, "ab")
	}
}

// TestIsOrphanCombiningMarkLeavesVariationSelectorsAlone guards the split the
// two paths agreed on: variation selectors are Unicode combining marks, but
// StripOrphanVariationSelectors owns them, so the orphan-mark rule must not
// claim them.
//
// Selectors belong to the variation-selector machinery whose bases Unicode
// defines (keycap "1\ufe0f\u20e3", symbol "❤️"); the orphan-mark rule's own
// whitespace/absent-base verdict must not reach them, or the two paths could
// disagree about the same rune. The presentation selectors U+FE0E/U+FE0F are
// additionally covered by isIgnorableRune, which folds them earlier in the
// loop by design — that is a separate tolerance rule, not the orphan-mark
// verdict under test here.
func TestIsOrphanCombiningMarkLeavesVariationSelectorsAlone(t *testing.T) {
	// Sanity: the fold still owns a genuine tokenizer artifact.
	if !isOrphanCombiningMark([]rune("### \u0304.2.1"), 4) {
		t.Fatal("isOrphanCombiningMark(U+0304 after a space) = false, want true")
	}
	cases := []struct {
		name string
		s    string
		i    int
	}{
		{"presentation selector on a digit", "1\ufe0f\u20e3", 1},
		{"enclosing keycap", "1\ufe0f\u20e3", 2},
		{"presentation selector on a symbol", "\u2764\ufe0f", 1},
	}
	for _, tc := range cases {
		rs := []rune(tc.s)
		if isOrphanCombiningMark(rs, tc.i) {
			t.Fatalf("%s: isOrphanCombiningMark(%q, %d) = true, want false (U+%04X belongs to StripOrphanVariationSelectors)", tc.name, tc.s, tc.i, rs[tc.i])
		}
	}
}

// TestOrphanCombiningMarkRuleKeepsVisibleBases guards the boundary the rule
// must not cross. Any visible base keeps its mark: a combining mark over a
// digit, symbol, or punctuation rune is indistinguishable from legitimate
// content (a U+0305 overline over a digit, a U+0338 overlay after a
// relation), so only a mark with whitespace — or nothing — before it is a
// provably floating tokenizer artifact. Enclosing marks (category Me) never
// enter the rule.
func TestOrphanCombiningMarkRuleKeepsVisibleBases(t *testing.T) {
	kept := []struct {
		name string
		s    string
		i    int
	}{
		{"mark over a letter", "a\u0304b", 1},
		{"mark over a digit", "1\u03042", 1},
		{"stacked marks over a digit", "1\u0304\u03052", 1},
		{"overline over a decimal digit", "0.3\u0305", 3},
		{"mark over a math symbol", "\u2192\u0305", 1},
		{"enclosing circle over a digit (Me)", "1\u20dd", 1},
		{"enclosing keycap (Me)", "1\ufe0f\u20e3", 2},
	}
	for _, tc := range kept {
		rs := []rune(tc.s)
		if isOrphanCombiningMark(rs, tc.i) {
			t.Fatalf("%s: isOrphanCombiningMark(%q, %d) = true, want false", tc.name, tc.s, tc.i)
		}
	}
	floating := []struct {
		name string
		s    string
		i    int
	}{
		{"leading mark", "\u0304ab", 0},
		{"mark after a space", "### \u0304.2.1", 4},
		{"mark after a newline", "\n\u0304a", 1},
		{"stacked marks on whitespace", " \u0302\u0301a", 2},
	}
	for _, tc := range floating {
		rs := []rune(tc.s)
		if !isOrphanCombiningMark(rs, tc.i) {
			t.Fatalf("%s: isOrphanCombiningMark(%q, %d) = false, want true", tc.name, tc.s, tc.i)
		}
	}
}

// TestNormalizePunctWithSpaceFoldingPreservesStackedCombiningMarks guards
// legitimate multi-mark diacritics: a base letter carrying two combining
// marks (Vietnamese ấ in NFD is a + U+0302 circumflex + U+0301 acute) must
// keep both marks, because the second mark's base is the letter under the
// earlier mark, not the mark itself. Folding it would corrupt the script.

func TestNormalizePunctWithSpaceFoldingPreservesStackedCombiningMarks(t *testing.T) {
	// a + U+0302 + U+0301 = ấ (NFD); both marks sit on the letter a.
	norm, _ := normalizePunctWithSpaceFolding([]rune("a\u0302\u0301b"))
	if got := string(norm); got != "a\u0302\u0301b" {
		t.Fatalf("norm = %q, want %q (stacked combining marks preserved)", got, "a\u0302\u0301b")
	}

	// Three marks on one base stay intact as well.
	norm2, _ := normalizePunctWithSpaceFolding([]rune("e\u0301\u0302\u0303"))
	if got := string(norm2); got != "e\u0301\u0302\u0303" {
		t.Fatalf("norm = %q, want %q (three stacked marks preserved)", got, "e\u0301\u0302\u0303")
	}

	// A mark after a digit is preserved: Unicode allows combining marks on
	// any visible base, and digits carry legitimate sequences (U+0305
	// overlines in math), so only a mark with whitespace or no base at all
	// is provably a floating artifact.
	norm3, _ := normalizePunctWithSpaceFolding([]rune("1\u03042"))
	if got := string(norm3); got != "1\u03042" {
		t.Fatalf("norm = %q, want %q (mark after digit preserved)", got, "1\u03042")
	}
}

// TestEditToolTolerantMatchFoldsOrphanCombiningMark guards the full Edit
// flow: a model copy of a heading that leaked an orphan U+0304 still
// replaces the file's clean text through the tolerant fallback.

func TestEditToolTolerantMatchFoldsOrphanCombiningMark(t *testing.T) {
	dir := t.TempDir()
	file := "## 3.2.1 Heading\nbody\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "## \u03043.2.1 Heading\n"
	newText := "## 3.2.1 Renamed\n"
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
	if want := "## 3.2.1 Renamed\nbody\n"; string(got) != want {
		t.Fatalf("file = %q, want %q (orphan U+0304 folded during match)", string(got), want)
	}
}

// TestEditToolStripsOrphanCombiningMarkFromNewString guards the write path:
// a model that leaks an orphan U+0304 into new_string must not have it written
// to the file. The old_string matches the file cleanly (no tolerance needed),
// but the orphan mark in the replacement is dropped from the bytes that land in
// the file and reported in the cleaned-invisible note.
func TestEditToolStripsOrphanCombiningMarkFromNewString(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "demo.md", "## 3.2.1 Heading\nbody\n")
	out, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": "## 3.2.1 Heading\n",
		"new_string": "## \u03043.2.1 Renamed\n",
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want success", err)
	}
	if !strings.Contains(out, "cleaned 1 invisible character") {
		t.Fatalf("output = %q, want cleaned-invisible note", out)
	}
	got, _ := os.ReadFile(path)
	if want := "## 3.2.1 Renamed\nbody\n"; string(got) != want {
		t.Fatalf("file = %q, want %q (orphan U+0304 dropped from new_string)", string(got), want)
	}
}

// TestEditToolKeepsCombiningMarkOnDigitInNewString guards the corrected
// write-path boundary: a combining mark over a digit in new_string is
// legitimate content (a U+0305 overline, for example) and must be written
// verbatim — the write-path strip only drops marks with whitespace or no
// base at all, never marks over a visible base.
func TestEditToolKeepsCombiningMarkOnDigitInNewString(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "demo.md", "## 3.2.1 Heading\nbody\n")
	out, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": "## 3.2.1 Heading\n",
		"new_string": "## 3\u03052.1 Renamed\n",
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want success", err)
	}
	if strings.Contains(out, "cleaned") {
		t.Fatalf("output = %q, want no cleaned-invisible note (digit mark kept)", out)
	}
	got, _ := os.ReadFile(path)
	if want := "## 3\u03052.1 Renamed\nbody\n"; string(got) != want {
		t.Fatalf("file = %q, want %q (digit mark kept in new_string)", string(got), want)
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

// The shared first-mismatch hint reports a 1-based rune offset and does not
// call a merely shorter line "empty": only a side with no rune at offset 0 is
// an empty line, while one that ran out later simply ends there.
func TestFirstMismatchHint(t *testing.T) {
	tests := []struct {
		name             string
		expected, actual string
		want             string
	}{
		{"identical", "abc", "abc", ""},
		{"rune differs", "abc", "axc", "first mismatch at rune 2 (1-based): your line has U+0062, file has U+0078"},
		{"actual is an empty line", "line", "", "first mismatch at rune 1 (1-based): your line has U+006C, file has no characters (line is empty)"},
		{"actual line ends earlier", "line2x", "line2", "first mismatch at rune 6 (1-based): your line has U+0078, file has no characters (line ends here)"},
		{"expected line ends earlier", "line2", "line2x", "first mismatch at rune 6 (1-based): your line has no characters (line ends here), file has U+0078"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstMismatchHint(tc.expected, tc.actual); got != tc.want {
				t.Fatalf("firstMismatchHint(%q, %q) = %q, want %q", tc.expected, tc.actual, got, tc.want)
			}
		})
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
		// Same-height, same-position replacements pair up as substitutions,
		// not drift: a retyped line is one line, not "one extra line each
		// side" that would misreport a line-count difference.
		{"single in-place replacement", []string{"a", "b", "c"}, []string{"a", "x", "c"}, 0, 0, false},
		{"two in-place replacements", []string{"a", "b1", "b2", "c"}, []string{"a", "x", "y", "c"}, 0, 0, false},
		// Surplus lines beyond the pairing are still counted as drift.
		{"replacement plus old surplus", []string{"a", "b", "c", "d"}, []string{"a", "x", "y"}, 1, 0, false},
		// Unmatched lines that do NOT sit in the same run (each side's
		// leftover falls on opposite sides of the shared anchor) are real
		// drift, not a substitution pair.
		{"displaced mismatch stays drift", []string{"a", "b", "c"}, []string{"a", "x", "b"}, 1, 1, false},
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

// IsApproximateMatchFailure classifies the drift/stale-read failure that a
// fresh bounded read fixes, and excludes failures that need different
// arguments (ambiguous multi-match) or more context instead.

func TestIsApproximateMatchFailure(t *testing.T) {
	editNotFound := errors.New("old_string not found in file, even after punctuation/whitespace tolerance. Closest match is at line 3 (96% similar, 5 character difference)")
	editNotFoundLargeDrift := errors.New("old_string not found in file, even after punctuation/whitespace tolerance. Whole lines drifted (4 vs 4 extra line(s)); read the file with offset=3 limit=9")
	editAmbiguous := errors.New("old_string found 2 times, provide more context or set replace_all to true")
	patchNotFound := errors.New("hunk not found (1/1); the expected text is only part of current line 3; the file may have changed")
	patchUnsafe := errors.New("hunk not found (1/1); a punctuation/whitespace-tolerant candidate exists at line 5, but the replacement cannot preserve unchanged text safely")
	validation := errors.New("args.path must be a string")

	cases := []struct {
		tool string
		name string
		err  error
		want bool
	}{
		{NameEdit, "edit not found", editNotFound, true},
		{NameEdit, "edit large drift", editNotFoundLargeDrift, true},
		{NameEdit, "edit ambiguous", editAmbiguous, false},
		{NameApplyPatch, "patch not found", patchNotFound, true},
		{NameApplyPatch, "patch unsafe", patchUnsafe, true},
		{NameEdit, "validation", validation, false},
	}
	for _, tc := range cases {
		if got := IsApproximateMatchFailure(tc.tool, tc.err); got != tc.want {
			t.Errorf("IsApproximateMatchFailure(%s, %s) = %v, want %v", tc.tool, tc.name, got, tc.want)
		}
	}
	if IsApproximateMatchFailure(NameApplyPatch, editNotFound) {
		t.Error("edit failure classified as apply_patch failure")
	}
	if IsApproximateMatchFailure(NameEdit, patchNotFound) {
		t.Error("apply_patch failure classified as edit failure")
	}
	if IsApproximateMatchFailure(NameEdit, nil) {
		t.Error("nil error classified as a match failure")
	}
}

// TestStripOrphanCombiningMarks guards the write-path half of the orphan-mark
// fold: the matching normalizer folds orphan combining marks during comparison,
// but a leaked mark in the text that actually gets written used to survive into
// the file. StripOrphanCombiningMarks removes it without touching marks that
// legitimately sit on a base character.
func TestStripOrphanCombiningMarks(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Tokenizer artifact on a copied heading: orphaned mark is dropped.
		{"### \u03043.2.1 Heading", "### 3.2.1 Heading"},
		// A mark after a digit is preserved: digits can carry legitimate
		// combining sequences (a U+0305 overline over a digit in math
		// notation), so a visible base is never proof of a model artifact.
		{"1\u03042", "1\u03042"},
		{"0.3\u0305", "0.3\u0305"},
		// A mark whose only base is whitespace is dropped — it is provably
		// floating (the "### [U+0304].2.1" heading artifact).
		{"a \u0304b", "a b"},
		{"a\u00a0\u0304b", "a\u00a0b"}, // NBSP is whitespace too
		// Enclosing marks (category Me) never enter the rule, even over
		// digits and symbols: U+20DD circle and U+20E3 keycap stay.
		{"1\u20dd2", "1\u20dd2"},
		{"x\u20dd", "x\u20dd"},
		// Leading orphan mark.
		{"\u0304ab", "ab"},
		// Mark after a letter is preserved (legitimate single diacritic).
		{"a\u0304b", "a\u0304b"},
		// Stacked marks on one letter survive intact (Vietnamese, Arabic).
		{"e\u0302\u0301", "e\u0302\u0301"},
		{"\u0645\u0651\u064E", "\u0645\u0651\u064E"},
		// A keycap emoji ("1\ufe0f\u20e3"): the variation selector and
		// enclosing keycap are combining marks but owned by
		// StripOrphanVariationSelectors and must NOT be stripped here.
		{"1\ufe0f\u20e3", "1\ufe0f\u20e3"},
		// Plain ASCII is untouched.
		{"package main", "package main"},
	}
	for _, tc := range cases {
		if got := StripOrphanCombiningMarks(tc.in); got != tc.want {
			t.Errorf("StripOrphanCombiningMarks(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCountStrippedInvisibleReportsCombiningMarks checks that a floating
// combining mark removed by the write-path strip is counted and reported the
// same way the zero-width set is, while a mark that is kept (over a visible
// base) never shows up.
func TestCountStrippedInvisibleReportsCombiningMarks(t *testing.T) {
	// Floating mark (only whitespace before it) removed: reported.
	if got := CountStrippedInvisible("### \u0304.2.1", "### .2.1"); got['\u0304'] != 1 {
		t.Errorf("floating mark not counted: %v", got)
	}
	// Kept mark (over a digit): nothing to report.
	if got := CountStrippedInvisible("1\u03042", "1\u03042"); len(got) != 0 {
		t.Errorf("kept mark over a digit should not be counted: %v", got)
	}
	// Kept mark (on a letter): nothing to report.
	if got := CountStrippedInvisible("a\u0304b", "a\u0304b"); len(got) != 0 {
		t.Errorf("kept mark should not be counted: %v", got)
	}
}

// The line-scoped normalizer folds runs of whitespace, so re-spacing an
// aligned assignment is invisible to apply_patch's exact/tolerant matching.
// Real content differences (an operator, a digit) must stay visible: before
// the fold those two classes were ranked backwards by the similarity ratio —
// harmless re-spacing scored 0.71..0.89 (refused by the tolerant layer, then
// resolved by the fuzzy layer) while an operator change scored 0.90+.
func TestNormalizePatchTolerantLineFoldsWhitespaceRuns(t *testing.T) {
	same := [][2]string{
		{"count   = 1", "count = 1"},
		{"x = 1", "x  =  1"},
		{"foo(a, b)", "foo(a,  b)"},
		{"    foo", "\tfoo"},
		{"a\t\tb", "a b"},
	}
	for _, p := range same {
		if got, want := normalizePatchTolerantLine(p[0]), normalizePatchTolerantLine(p[1]); got != want {
			t.Errorf("normalizePatchTolerantLine(%q) = %q, want it to equal normalizePatchTolerantLine(%q) = %q", p[0], got, p[1], want)
		}
	}
	differ := [][2]string{
		{"if a >= b {", "if a > b {"},
		{"const n = 30", "const n = 10"},
	}
	for _, p := range differ {
		if got, other := normalizePatchTolerantLine(p[0]), normalizePatchTolerantLine(p[1]); got == other {
			t.Errorf("normalizePatchTolerantLine(%q) == normalizePatchTolerantLine(%q) = %q, want a real content change to survive normalization", p[0], p[1], got)
		}
	}
}

// The fold is line-scoped on purpose. Edit's old_string spans lines, so at the
// sequence level a whitespace run is indentation, alignment, or list
// structure; folding it there would let an edit match a block whose layout it
// misquoted. Line terminators are never part of a run either way.
func TestNormalizePunctWithSpaceFoldingKeepsWhitespaceRuns(t *testing.T) {
	seq := func(s string) string {
		norm, _ := normalizePunctWithSpaceFolding([]rune(s))
		return string(norm)
	}
	if seq("a  b") == seq("a b") {
		t.Errorf("sequence-level normalization folded a double space; Edit keeps whitespace runs significant")
	}
	if got := seq("alpha\n\nbeta"); got != "alpha\n\nbeta" {
		t.Errorf("seq(%q) = %q, want blank lines preserved", "alpha\n\nbeta", got)
	}
	line := func(s string) string {
		norm, _ := normalizePunctLineWithSpaceFolding([]rune(s))
		return string(norm)
	}
	if got := line("alpha\n\nbeta"); got != "alpha\n\nbeta" {
		t.Errorf("line(%q) = %q, want line terminators excluded from whitespace runs", "alpha\n\nbeta", got)
	}
}

// Splice-back indexes the original runes through the spans, so a folded run
// must be carried entirely by the span of the rune that replaced it: the spans
// have to tile the input contiguously or a tolerant replacement writes
// mangled bytes.
func TestNormalizePunctLineWithSpaceFoldingSpansTileInput(t *testing.T) {
	for _, in := range []string{"count   = 1", "foo(a,  b)", "\t\tif x  ==  y {", "a  ", "  a", "  "} {
		rs := []rune(in)
		norm, spans := normalizePunctLineWithSpaceFolding(rs)
		if len(norm) != len(spans) {
			t.Fatalf("%q: %d normalized runes but %d spans", in, len(norm), len(spans))
		}
		if len(spans) == 0 {
			continue
		}
		if spans[0].start != 0 || spans[len(spans)-1].end != len(rs) {
			t.Errorf("%q: spans cover [%d,%d), want [0,%d)", in, spans[0].start, spans[len(spans)-1].end, len(rs))
		}
		for i := 1; i < len(spans); i++ {
			if spans[i-1].end != spans[i].start {
				t.Errorf("%q: span gap/overlap between %v and %v", in, spans[i-1], spans[i])
			}
		}
	}
}
