package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
)

// GrepTool searches file contents using a regex pattern.
type GrepTool struct {
	BaseDir string // session working directory for relative paths; empty keeps process cwd behavior
	// WorktreeRoot, when set, is the absolute directory that holds
	// chord-managed worktrees. A walk rooted above it prunes it so a search
	// never reports another checkout's copies; searching inside it explicitly
	// still works.
	WorktreeRoot string
}

type grepArgs struct {
	Pattern  string   `json:"pattern"`
	Paths    []string `json:"paths,omitempty"`
	Includes []string `json:"includes,omitempty"`
	// ContextLines is the number of surrounding lines kept around every hit.
	// 0 (the default) keeps the historical match-only output byte for byte;
	// a positive value adds up to that many lines before and after each hit,
	// with adjacent hit windows merged so a shared line is emitted once.
	ContextLines int `json:"context_lines,omitempty"`
	// RequestedContextLines is the decoded value the caller sent, kept
	// unclamped so the clamp note can quote it.
	RequestedContextLines float64 `json:"-"`
	// LiteralPatterns are the supplied patterns that were not valid regexes and
	// were quoted into literal text, in the order they were given.
	LiteralPatterns []string `json:"-"`
	PathsCoerced    bool     `json:"-"`
	IncludesCoerced bool     `json:"-"`
}

// UnmarshalJSON accepts a string or array of strings for paths, includes, and
// the plural "patterns" alias of pattern, recording whether a scalar was
// coerced into a single-element list. This keeps strict array semantics in the
// documented schema while preventing hard failures when models supply a single
// string by habit. The canonical "pattern" field itself stays a single string:
// a list under it is a type error, and the plural patterns list means "a line
// matches when any of them does", so it decodes to the alternation of the
// individual regexes.
func (a *grepArgs) UnmarshalJSON(data []byte) error {
	var raw struct {
		Pattern  json.RawMessage `json:"pattern"`
		Patterns json.RawMessage `json:"patterns,omitempty"`
		Paths    json.RawMessage `json:"paths,omitempty"`
		Includes json.RawMessage `json:"includes,omitempty"`
		Path     json.RawMessage `json:"path,omitempty"`
		Glob     json.RawMessage `json:"glob,omitempty"`
		// ContextLines is decoded by hand so an integral float such as 2.0,
		// which the integer schema admits, is accepted here too.
		ContextLines json.RawMessage `json:"context_lines,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Accept deprecated singular/plural spellings when their current
	// counterparts are absent so habit-shaped calls still work; current fields
	// always win.
	if len(raw.Paths) == 0 {
		raw.Paths = raw.Path
	}
	if len(raw.Includes) == 0 {
		raw.Includes = raw.Glob
	}
	var pattern []string
	if len(raw.Pattern) == 0 {
		// Canonical field absent: fall back to the tolerated plural alias,
		// whose list means "match when any pattern matches" (an alternation).
		var err error
		pattern, _, err = DecodeStringOrList(raw.Patterns)
		if err != nil {
			return fmt.Errorf("patterns: %w", err)
		}
	} else {
		// Canonical "pattern" is a single string; a list under it is a type
		// error — the plural "patterns" field is where lists belong.
		var single string
		if err := json.Unmarshal(raw.Pattern, &single); err != nil {
			return fmt.Errorf("pattern: expected a single string; an array of patterns belongs under the plural \"patterns\" field")
		}
		pattern = []string{single}
	}
	paths, pathsCoerced, err := DecodeStringOrList(raw.Paths)
	if err != nil {
		return fmt.Errorf("paths: %w", err)
	}
	includes, includesCoerced, err := DecodeStringOrList(raw.Includes)
	if err != nil {
		return fmt.Errorf("includes: %w", err)
	}
	a.Pattern, a.LiteralPatterns = grepPatternAlternation(pattern)
	a.Paths = paths
	a.Includes = includes
	a.PathsCoerced = pathsCoerced
	a.IncludesCoerced = includesCoerced
	requested, err := decodeGrepContextLines(raw.ContextLines)
	if err != nil {
		return err
	}
	a.RequestedContextLines = requested
	// Values beyond the display limit are only clamped; bounding them first
	// keeps the int conversion defined for arbitrarily large numbers.
	a.ContextLines = min(int(min(requested, math.MaxInt32)), maxGrepContextLines)
	return nil
}

// decodeGrepContextLines decodes the optional context_lines argument with the
// same integer rule the schema applies: an absent field and an explicit null
// mean the default (0, match-only output), and any integral JSON number —
// including a float spelling such as 2.0 — is accepted. The returned value is
// the number the caller sent; Execute bounds the int conversion, clamps
// positive values to maxGrepContextLines, and reports the clamp with this
// value.
func decodeGrepContextLines(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var value *float64
	if err := json.Unmarshal(raw, &value); err != nil || (value != nil && math.Trunc(*value) != *value) {
		return 0, fmt.Errorf("context_lines: expected a non-negative integer")
	}
	if value == nil {
		return 0, nil
	}
	if *value < 0 {
		return 0, fmt.Errorf("context_lines must be non-negative; got %v", *value)
	}
	return *value, nil
}

// grepPatternAlternation combines the supplied patterns into the single regexp
// source meaning "a line matches when any of them does", and reports which of
// them had to be searched as literal text. It is the one place that decides
// what a list of patterns means: both the plural alias shaper (which runs at
// validation time) and grepArgs decoding call it, so the schema-visible value
// and the executed pattern can never drift apart.
//
// Each element is wrapped in a non-capturing group before joining, so the
// alternation binds per element and an inline flag group such as "(?i)" stays
// scoped to the pattern that carries it instead of leaking into its
// neighbours. Empty elements are dropped: a bare alternation arm matches every
// line, which would report arbitrary lines of the whole tree as matches.
// Elements that do not compile are quoted individually, so one unparseable
// pattern (typically pasted source such as "func Foo(") degrades to a literal
// search of itself while the remaining elements keep their regex meaning.
//
// The combined source is empty when no usable pattern remains; callers report
// that as a missing pattern.
func grepPatternAlternation(patterns []string) (combined string, literal []string) {
	parts := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		source := pattern
		if _, err := regexp.Compile(source); err != nil {
			source = regexp.QuoteMeta(source)
			literal = append(literal, pattern)
		}
		parts = append(parts, source)
	}
	switch len(parts) {
	case 0:
		return "", nil
	case 1:
		// A lone pattern needs no grouping: keeping the caller's own spelling
		// keeps search logs and error text showing what was actually written.
		return parts[0], literal
	}
	for i, part := range parts {
		parts[i] = "(?:" + part + ")"
	}
	return strings.Join(parts, "|"), literal
}

// grepLiteralFallbackNote describes which patterns were searched as literal
// text, or returns an empty string when every pattern compiled as a regex.
func grepLiteralFallbackNote(literal []string) string {
	switch len(literal) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("pattern %q was invalid regex; searched as literal text", literal[0])
	default:
		quoted := make([]string, 0, len(literal))
		for _, pattern := range literal {
			quoted = append(quoted, fmt.Sprintf("%q", pattern))
		}
		return "patterns " + strings.Join(quoted, ", ") + " were invalid regex; searched as literal text"
	}
}

const (
	maxGrepMatches     = 120
	maxGrepOutputBytes = 12 * 1024
	// maxGrepContextLines bounds context_lines; larger requests are clamped.
	maxGrepContextLines = 20
	// maxGrepContextLineBytes shortens long surrounding lines (minified or
	// generated code) so one of them cannot exhaust the output budget that
	// the matches share.
	maxGrepContextLineBytes = 256
)

// GrepContextOmittedFooterPrefix starts the result footer grep appends when
// the output budget ran out for surrounding lines: every window before that
// point is complete, and later matches are listed without context.
const GrepContextOmittedFooterPrefix = "(surrounding lines omitted"

// grepContextLinesDescription is the single statement of the context_lines
// rules; the tool description only points at the parameter.
var grepContextLinesDescription = fmt.Sprintf("Optional number of lines to return before and after each matching line (0-%d, default 0; larger values are clamped to %d with a note)."+
	" Surrounding lines are rendered as `| path-line-text` while matches keep `path:line:text`; windows of nearby matches are merged, so a shared line appears once, and surrounding lines longer than %d bytes are shortened with `...`."+
	" Surrounding lines count against the output budget but not against the match cap; once the budget cannot fit more of them, later matches are listed without context and a footer says so.",
	maxGrepContextLines, maxGrepContextLines, maxGrepContextLineBytes)

func (GrepTool) Name() string { return NameGrep }

func (t GrepTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameGrep, pathsToolConcurrencyPolicyInDir(args, "paths", t.BaseDir))
}

func (GrepTool) Description() string {
	return "Search file contents using a regular expression. If pattern is not valid regex, it is safely searched as literal text and the result reports that fallback." +
		" Use paths for one or more files/directories and includes for optional path globs; single bare strings are tolerated for either, but arrays are preferred." +
		" If the exact file path is known, pass the full file path in paths instead of searching its parent directory with the filename in includes; includes filters files during traversal and does not avoid walking the search path." +
		" Returns matching lines with file paths and line numbers." +
		" Optional context_lines also returns the lines around each match." +
		" Surrounding lines are partial excerpts that may be shortened or omitted under output limits, so read the file before editing it." +
		" Best for discovering candidate files, symbols, or text matches when the exact location is not known yet."
}

func (GrepTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regular expression for file contents.",
			},
			"paths": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"description":      "One or more files/directories to search (JSON array, e.g. [\"internal\", \"cmd\"]). Relative paths resolve from the session working directory. Supports ~ for the current user's home directory. Defaults to the session working directory when omitted.",
				"coerceFromString": true,
			},
			"includes": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"description":      "Optional path glob filters relative to each searched directory, as a JSON array (e.g. [\"**/*.go\"] or [\"internal/**/*.ts\", \"cmd/**/*.ts\"]). Omit to search all non-ignored text files.",
				"coerceFromString": true,
			},
			"context_lines": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": grepContextLinesDescription,
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
}

func (GrepTool) IsReadOnly() bool { return true }

func (GrepTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (GrepTool) CanRenderBeforeToolUseEnd(json.RawMessage) bool { return true }

// argumentAliases maps tolerated singular/plural field names to the canonical
// schema fields so model-generated variants validate without exposing the
// alternate names in Parameters(). The plural "patterns" mirrors glob's field
// name and carries an array, which shapeAliasArgument collapses into the
// canonical single pattern string before the alias is renamed.
func (GrepTool) argumentAliases() map[string]string {
	return map[string]string{"path": "paths", "glob": "includes", "patterns": "pattern"}
}

// shapeAliasArgument implements aliasArgumentValueShaper for the plural
// "patterns" alias: a list of patterns means "match a line when any of them
// matches", which for a line-oriented search is exactly the alternation of the
// individual regexes built by grepPatternAlternation. A list of strings is
// collapsed into that single source; anything else (a scalar, an empty list, a
// non-string element) is left untouched so ordinary type validation reports it
// instead of guessing. A list whose entries are all empty strings collapses to
// an empty pattern, which the executor reports as a missing pattern. The shaper
// only fires for the alias key as written by the model: the canonical "pattern"
// field is never shaped and keeps its declared single-string type.
func (GrepTool) shapeAliasArgument(aliasKey string, value any) (any, bool) {
	if aliasKey != "patterns" {
		return nil, false
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		part, ok := item.(string)
		if !ok {
			return nil, false
		}
		parts = append(parts, part)
	}
	combined, _ := grepPatternAlternation(parts)
	return combined, true
}

func (t GrepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	startedAt := time.Now()
	var a grepArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	// grepPatternAlternation already quoted every element that is not a valid
	// regex, so a failure here is the combined expression exceeding regexp's
	// own limits. Report it instead of quoting the whole alternation, which
	// would search for a literal string no file can contain.
	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return "", fmt.Errorf("compile search pattern: %w", err)
	}
	literalNote := grepLiteralFallbackNote(a.LiteralPatterns)

	var matches []string
	var matchCount int
	var outputBytes int
	var scannedFiles int64
	truncated := false
	contextOmitted := false
	paths := grepSearchPaths(a, t.BaseDir)
	includes := grepIncludes(a)
	searched := make([]string, 0, len(paths))

	var pathErrors []string
	for _, searchPath := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		resolvedSearchPath, info, err := resolveExistingToolPathInDir(searchPath, t.BaseDir, PathTargetAny, "search")
		if err != nil {
			pathErrors = append(pathErrors, grepPathErrorWithHint(searchPath, t.BaseDir, err).Error())
			continue
		}
		searched = append(searched, resolvedSearchPath)
		skipDir := ""
		if rel, ok := worktreeSkipRel(resolvedSearchPath, t.WorktreeRoot); ok {
			skipDir = filepath.Join(resolvedSearchPath, rel)
		}
		remainingBytes := maxGrepOutputBytes - outputBytes
		if len(matches) > 0 {
			remainingBytes--
		}
		// Once an earlier root ran out of room for context, later roots list
		// bare matches, the same as later files within one root.
		contextLines := a.ContextLines
		if contextOmitted {
			contextLines = 0
		}
		root, err := grepSearchRoot(ctx, searchPath, resolvedSearchPath, info, re, includes, t.BaseDir, maxGrepMatches-matchCount, remainingBytes, contextLines, skipDir)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			pathErrors = append(pathErrors, fmt.Sprintf("%s: %v", resolvedSearchPath, err))
			continue
		}
		if len(matches) > 0 && len(root.lines) > 0 {
			outputBytes++
		}
		matches = append(matches, root.lines...)
		matchCount += root.hits
		outputBytes += root.bytes
		scannedFiles += root.scanned
		contextOmitted = contextOmitted || root.contextOmitted
		if root.truncated || matchCount >= maxGrepMatches || outputBytes >= maxGrepOutputBytes {
			truncated = true
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// Every path failed to resolve/search: return the aggregate error. Judge by
	// whether all paths errored, not by match count, otherwise a successful but
	// empty search plus one failed path would be misreported as all-failed.
	if len(pathErrors) > 0 && len(pathErrors) == len(paths) {
		return "", fmt.Errorf("all search paths failed: %s. The path may be stale or relative to a different working directory. Verify the current working directory or use a discovery tool (glob/grep from the repo root) to locate the target before retrying. Do not guess a similar-looking path", strings.Join(pathErrors, "; "))
	}

	filter := strings.Join(includes, ",")
	searchLabel := strings.Join(searched, ",")
	notes := grepCoerceNotes(a)
	if a.RequestedContextLines > maxGrepContextLines {
		notes = append(notes, fmt.Sprintf("Note: context_lines %v exceeds the maximum of %d; using %d.", a.RequestedContextLines, maxGrepContextLines, a.ContextLines))
	}
	// Append per-path failures as notes when partial results exist.
	for _, pe := range pathErrors {
		notes = append(notes, "grep: skipped path: "+pe)
	}
	if len(matches) == 0 {
		logSlowSearch("Grep", searchLabel, a.Pattern, filter, startedAt, "scanned_files", int(scannedFiles), 0, truncated)
		msg := "No matches found."
		if literalNote != "" {
			msg = "No matches found. (" + literalNote + ")"
		}
		msg += " If the symbol or phrase is expected, try alternate naming, a narrower literal, or broaden the search scope (paths/includes) before assuming absence."
		return prependNotes(notes, msg), nil
	}

	// Safety net: every hit contributes at most itself plus the lines of its
	// context window, so this bound can only fire if a root over-reported.
	if maxLines := maxGrepMatches * (1 + 2*a.ContextLines); len(matches) > maxLines {
		matches = grepCutAtHitBoundary(matches, maxLines)
	}

	result := strings.Join(matches, "\n")
	if literalNote != "" {
		result = "Note: " + literalNote + ".\n" + result
	}
	result = prependNotes(notes, result)
	// The two footers are independent: matches can be cut while every window
	// stayed complete, and context can run out while every match was listed.
	footerSep := "\n\n"
	if truncated {
		result += fmt.Sprintf("%s(showing first %d matches within %d KiB; narrow paths/includes/pattern for more precise results)", footerSep, matchCount, maxGrepOutputBytes/1024)
		footerSep = "\n"
	}
	if contextOmitted {
		result += footerSep + fmt.Sprintf("%s for later matches to stay within %d KiB; lower context_lines or narrow paths/includes/pattern to see them)", GrepContextOmittedFooterPrefix, maxGrepOutputBytes/1024)
	}
	logSlowSearch("Grep", searchLabel, a.Pattern, filter, startedAt, "scanned_files", int(scannedFiles), matchCount, truncated)
	return result, nil
}

func grepSearchPaths(a grepArgs, baseDir string) []string {
	paths := normalizeStringList(a.Paths)
	if len(paths) == 0 {
		if strings.TrimSpace(baseDir) != "" {
			paths = []string{baseDir}
		} else {
			paths = []string{"."}
		}
	}
	return paths
}

func grepIncludes(a grepArgs) []string {
	return normalizeStringList(a.Includes)
}

func grepCoerceNotes(a grepArgs) []string {
	var notes []string
	if a.PathsCoerced {
		notes = append(notes, `Note: paths was a string and was treated as a one-item list. Use the documented array form, for example paths: ["internal/tui"], in future calls.`)
	}
	if a.IncludesCoerced {
		notes = append(notes, `Note: includes was a string and was treated as a one-item list. Use the documented array form, for example includes: ["**/*.go"], in future calls.`)
	}
	return notes
}

func prependNotes(notes []string, body string) string {
	if len(notes) == 0 {
		return body
	}
	return strings.Join(notes, "\n") + "\n" + body
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// DecodeStringOrList decodes a JSON value that may be either a single string or
// an array of strings. coerced is true when the caller supplied a bare string,
// so the executor can attach a result-level hint nudging the documented array
// shape and permission/display layers can reproduce the same scalar->array
// coercion instead of falling back to a wildcard argument. An empty/missing
// field returns (nil, false, nil).
func DecodeStringOrList(raw json.RawMessage) ([]string, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return nil, false, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, false, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, false, err
	}
	return []string{single}, true, nil
}

// grepRootResult is the outcome of searching one root: the formatted output
// lines, the number of real hits among them (context lines are excluded), the
// output bytes consumed, the number of files scanned, whether matches were
// cut by the budgets, and whether surrounding lines were omitted. The hit
// count is reported separately from len(lines) because a context_lines search
// emits surrounding lines that share the byte budget without being hits.
type grepRootResult struct {
	lines          []string
	hits           int
	bytes          int
	scanned        int64
	truncated      bool
	contextOmitted bool
}

// grepSearchRoot searches one root within the remaining match and byte
// budgets.
func grepSearchRoot(ctx context.Context, searchPath, resolvedSearchPath string, info os.FileInfo, re *regexp.Regexp, includes []string, baseDir string, maxMatches, maxBytes, contextLines int, skipDir string) (grepRootResult, error) {
	if err := ctx.Err(); err != nil {
		return grepRootResult{}, err
	}
	if maxMatches <= 0 || maxBytes <= 0 {
		return grepRootResult{truncated: true}, nil
	}
	if !info.IsDir() {
		if err := ensureRegularFilePath(searchPath, info); err != nil {
			return grepRootResult{}, err
		}
		if IsBinaryExtension(filepath.Base(resolvedSearchPath)) {
			return grepRootResult{scanned: 1}, nil
		}
		scan := scanGrepFile(ctx, resolvedSearchPath, baseDir, re, maxMatches, maxBytes, contextLines)
		if scan.err != nil {
			return grepRootResult{}, scan.err
		}
		lines, appended := appendBudgetedGrepMatches(nil, scan, maxMatches, maxBytes, false)
		reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: 1})
		// A scan that stopped at its own caps is truncated even when the last
		// entry happened to fit: the file still has hits the budget dropped.
		return grepRootResult{
			lines:          lines,
			hits:           appended.hits,
			bytes:          appended.bytes,
			scanned:        1,
			truncated:      appended.truncated || scan.hitCaps,
			contextOmitted: appended.contextOff,
		}, nil
	}

	// Fast path: when includes contains a relative path with no glob metacharacters
	// (e.g. "architecture-review-20260621-064007.html" or "src/main.go"), try the
	// exact file under the search root before recursively walking it. This avoids
	// walking huge roots (like the system temp directory) when the caller already
	// knows the relative path.
	if exactFiles, ok := resolveExactIncludeFiles(resolvedSearchPath, includes); ok {
		var res grepRootResult
		for _, file := range exactFiles {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			remainingMatches := maxMatches - res.hits
			remainingBytes := maxBytes - res.bytes
			if len(res.lines) > 0 {
				remainingBytes--
			}
			if remainingMatches <= 0 || remainingBytes <= 0 {
				res.truncated = true
				break
			}
			info, err := os.Stat(file)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if IsBinaryExtension(filepath.Base(file)) {
				continue
			}
			scan := scanGrepFile(ctx, file, baseDir, re, remainingMatches, remainingBytes, contextLines)
			if scan.err != nil {
				res.truncated = false
				res.lines = nil
				return res, scan.err
			}
			prevLen := len(res.lines)
			var appended grepAppendResult
			res.lines, appended = appendBudgetedGrepMatches(res.lines, scan, remainingMatches, remainingBytes, res.contextOmitted)
			res.hits += appended.hits
			res.contextOmitted = appended.contextOff
			if prevLen > 0 && len(res.lines) > prevLen {
				res.bytes++
			}
			res.bytes += appended.bytes
			res.scanned++
			if appended.truncated || scan.hitCaps {
				res.truncated = true
				break
			}
		}
		if res.scanned > 0 {
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: res.scanned})
		}
		return res, nil
	}

	return grepWalkRoot(ctx, resolvedSearchPath, re, includes, baseDir, maxMatches, maxBytes, contextLines, skipDir)
}

// errGrepWalkCanceled stops the walker when the merger has already filled its
// output budgets; it is a clean stop, not an error surfaced to the caller.
var errGrepWalkCanceled = errors.New("grep walk canceled")

type grepWalkItem struct {
	idx  int
	path string
}

type grepScanResult struct {
	idx  int
	scan grepFileScan
}

type grepFileScanner func(ctx context.Context, path, baseDir string, re *regexp.Regexp, capMatches, capBytes int) grepFileScan

// grepScanWorkerCount bounds the parallel file-scan workers. Scanning is
// CPU-bound (regex over file contents), so GOMAXPROCS is the natural ceiling;
// the cap keeps a wide machine from issuing excessive concurrent file reads.
func grepScanWorkerCount() int {
	n := max(runtime.GOMAXPROCS(0), 1)
	return min(n, 8)
}

func grepScanWindow(workerCount int) int {
	return max(workerCount*2, 1)
}

// grepWalkRoot walks the tree sequentially (gitignore, includes, and guard
// checks stay single-threaded) while scanning candidate files on parallel
// workers. Results are merged strictly in walk order and output budgets are
// applied only at merge time, so matches, truncation markers, and ordering
// are identical to a sequential scan; workers only ever over-scan files whose
// results end up discarded after the budget fills, and cancellation stops the
// walk promptly.
func grepWalkRoot(ctx context.Context, resolvedSearchPath string, re *regexp.Regexp, includes []string, baseDir string, maxMatches, maxBytes, contextLines int, skipDir string) (grepRootResult, error) {
	// The scanner interface stays context-free so an injected test scanner keeps
	// compiling; the requested window is bound here instead.
	scanFile := func(ctx context.Context, path, baseDir string, re *regexp.Regexp, capMatches, capBytes int) grepFileScan {
		return scanGrepFile(ctx, path, baseDir, re, capMatches, capBytes, contextLines)
	}
	return grepWalkRootWithScanner(ctx, resolvedSearchPath, re, includes, baseDir, maxMatches, maxBytes, skipDir, scanFile)
}

func grepWalkRootWithScanner(ctx context.Context, resolvedSearchPath string, re *regexp.Regexp, includes []string, baseDir string, maxMatches, maxBytes int, skipDir string, scanFile grepFileScanner) (grepRootResult, error) {
	parentCtx := ctx
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	workers := grepScanWorkerCount()
	items := make(chan grepWalkItem, workers*2)
	results := make(chan grepScanResult, workers*2)
	dispatchSlots := make(chan struct{}, grepScanWindow(workers))

	ignore := newGitIgnoreMatcher(resolvedSearchPath)
	guard := newBroadSearchGuard("Grep", resolvedSearchPath, "includes", includes)

	walkErrCh := make(chan error, 1)
	go func() {
		defer close(items)
		nextIdx := 0
		walkErrCh <- filepath.WalkDir(resolvedSearchPath, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			guard.visit()
			if guard.shouldAbort() {
				return errGuardAbort
			}
			if d.IsDir() && skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			if skipDir != "" && d.IsDir() && path == skipDir {
				return filepath.SkipDir
			}
			if d.IsDir() {
				if rel, err := filepath.Rel(resolvedSearchPath, path); err == nil {
					rel = filepath.ToSlash(rel)
					if ignore.Match(rel, true) {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if rel, err := filepath.Rel(resolvedSearchPath, path); err == nil {
				rel = filepath.ToSlash(rel)
				if ignore.Match(rel, false) {
					return nil
				}
				if len(includes) > 0 {
					matched, matchErr := matchAnyIncludePattern(rel, includes)
					if matchErr != nil || !matched {
						return nil
					}
				}
			}
			if !d.Type().IsRegular() || IsBinaryExtension(d.Name()) {
				return nil
			}
			guard.candidate()
			select {
			case dispatchSlots <- struct{}{}:
			case <-ctx.Done():
				return errGrepWalkCanceled
			}
			select {
			case items <- grepWalkItem{idx: nextIdx, path: path}:
				nextIdx++
				return nil
			case <-ctx.Done():
				<-dispatchSlots
				return errGrepWalkCanceled
			}
		})
	}()

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for item := range items {
				if ctx.Err() != nil {
					continue // keep draining so the walker never blocks
				}
				scan := scanFile(ctx, item.path, baseDir, re, maxMatches, maxBytes)
				select {
				case results <- grepScanResult{idx: item.idx, scan: scan}:
				case <-ctx.Done():
				}
			}
		})
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var res grepRootResult
	budgetDone := false

	process := func(scan grepFileScan) {
		if budgetDone || scan.err != nil {
			return
		}
		remainingMatches := maxMatches - res.hits
		remainingBytes := maxBytes - res.bytes
		if len(res.lines) > 0 {
			remainingBytes--
		}
		if remainingMatches <= 0 || remainingBytes <= 0 {
			res.truncated = true
			budgetDone = true
			cancel()
			return
		}
		prevLen := len(res.lines)
		var appended grepAppendResult
		res.lines, appended = appendBudgetedGrepMatches(res.lines, scan, remainingMatches, remainingBytes, res.contextOmitted)
		res.hits += appended.hits
		res.contextOmitted = appended.contextOff
		if prevLen > 0 && len(res.lines) > prevLen {
			res.bytes++
		}
		res.bytes += appended.bytes
		res.scanned++
		if res.scanned <= 5 || res.scanned%10 == 0 {
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: res.scanned})
		}
		if appended.truncated || scan.hitCaps || res.hits >= maxMatches || res.bytes >= maxBytes {
			res.truncated = true
			budgetDone = true
			cancel()
		}
	}

	pending := make(map[int]grepFileScan)
	next := 0
	for scanRes := range results {
		pending[scanRes.idx] = scanRes.scan
		for {
			scan, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			next++
			process(scan)
			<-dispatchSlots
		}
	}

	walkErr := <-walkErrCh
	failed := grepRootResult{hits: res.hits, bytes: res.bytes, scanned: res.scanned, truncated: res.truncated}
	if err := parentCtx.Err(); err != nil {
		return failed, err
	}
	switch {
	case walkErr == nil:
	case errors.Is(walkErr, errGrepWalkCanceled):
	case errors.Is(walkErr, errGuardAbort):
		return failed, guard.abortError()
	default:
		return failed, fmt.Errorf("walking directory: %w", walkErr)
	}
	if res.scanned > 0 {
		reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: res.scanned})
	}
	return res, nil
}

func grepPathErrorWithHint(path string, baseDir string, err error) error {
	if err == nil || !strings.Contains(err.Error(), "path not found:") || !strings.ContainsAny(path, " \t\n\r") {
		return err
	}
	parts := strings.Fields(path)
	if len(parts) < 2 {
		return err
	}
	for _, part := range parts {
		resolved, resolveErr := resolveToolPathInDir(part, baseDir)
		if resolveErr != nil {
			return err
		}
		if _, statErr := os.Stat(resolved); statErr != nil {
			return err
		}
	}
	return fmt.Errorf("%w. grep.paths accepts an array of file or directory paths; to search multiple directories, pass each path as a separate array item", err)
}

// grepLineKind tells a hit from the surrounding lines kept for it. Leading and
// trailing context are distinguished because the budget treats them
// differently: a hit's leading lines enter the output together with the hit or
// not at all, so a window never ends in context whose hit was cut, while
// trailing lines follow their hit one by one.
type grepLineKind uint8

const (
	grepLineHit grepLineKind = iota
	grepLineLeading
	grepLineTrailing
)

// grepLineMatch is one output line from a scanned file, kept as components so
// budget application can reformat (and truncate) it.
type grepLineMatch struct {
	num  int
	text string // sanitized display text
	// kind marks surrounding lines kept only because a hit needed them.
	// Context entries share the output byte budget but never the hit budget:
	// counting them as hits would shrink the number of real matches a search
	// may report.
	kind grepLineKind
}

func (m grepLineMatch) isContext() bool { return m.kind != grepLineHit }

// grepFileScan is the outcome of scanning one file: the display path, the
// output lines in file order, whether the scan stopped at its caps, where it
// stopped keeping context, and any open/read error. A scan that stopped at
// caps includes the first match that overflowed the byte cap so the budget
// layer can apply the same head-truncation rule a direct scan would.
type grepFileScan struct {
	displayPath string
	matches     []grepLineMatch
	hitCaps     bool
	// contextOmitted reports that the scan's own byte cap ran out for
	// surrounding lines; entries from index contextCut on carry no context.
	contextOmitted bool
	contextCut     int
	err            error
}

// grepScanBuffers holds per-scan reusable allocations: the binary-detection
// head sample, the line scanner's initial buffer, and the slots of the
// leading-context ring.
type grepScanBuffers struct {
	head        []byte
	scan        []byte
	contextRaw  [][]byte
	contextNums []int
}

var grepScanBufPool = sync.Pool{
	New: func() any {
		return &grepScanBuffers{
			head: make([]byte, binarySampleBytes),
			scan: make([]byte, 0, 64*1024),
		}
	},
}

// grepContextRing keeps the most recent non-matching lines after the last
// emitted line as raw bytes, so a hit can emit its leading context while lines
// that never become context are neither converted to strings nor sanitized.
// Slots are reused across lines and, through grepScanBuffers, across files.
type grepContextRing struct {
	lines [][]byte
	nums  []int
	start int
	n     int
}

func newGrepContextRing(bufs *grepScanBuffers, size int) grepContextRing {
	for len(bufs.contextRaw) < size {
		bufs.contextRaw = append(bufs.contextRaw, nil)
	}
	if cap(bufs.contextNums) < size {
		bufs.contextNums = make([]int, size)
	}
	return grepContextRing{lines: bufs.contextRaw[:size], nums: bufs.contextNums[:size]}
}

func (r *grepContextRing) push(num int, line []byte) {
	idx := (r.start + r.n) % len(r.lines)
	if r.n == len(r.lines) {
		r.start = (r.start + 1) % len(r.lines)
	} else {
		r.n++
	}
	// One byte past the display limit is enough for grepContextText to know
	// the line must be shortened; the rest is never shown.
	r.lines[idx] = append(r.lines[idx][:0], line[:min(len(line), maxGrepContextLineBytes+1)]...)
	r.nums[idx] = num
}

// appendTo appends the buffered lines, in file order, as leading context and
// returns their total formatted length.
func (r *grepContextRing) appendTo(dst []grepLineMatch, displayPath string) ([]grepLineMatch, int) {
	total := 0
	for i := range r.n {
		idx := (r.start + i) % len(r.lines)
		entry := grepLineMatch{num: r.nums[idx], text: grepContextText(r.lines[idx]), kind: grepLineLeading}
		total += grepMatchLineLen(displayPath, entry)
		dst = append(dst, entry)
	}
	return dst, total
}

func (r *grepContextRing) reset() {
	r.start = 0
	r.n = 0
}

// grepContextText sanitizes a surrounding line for output, shortening it to
// maxGrepContextLineBytes on a rune boundary.
func grepContextText(raw []byte) string {
	if len(raw) <= maxGrepContextLineBytes {
		return sanitizeGrepLine(string(raw))
	}
	cut := maxGrepContextLineBytes
	for cut > 0 && !utf8.RuneStart(raw[cut]) {
		cut--
	}
	return sanitizeGrepLine(string(raw[:cut])) + "..."
}

// grepOutputBudget applies the match and byte limits to output lines in file
// order, counting one separator byte before every line but the first. Context
// is admitted only while it fits; once a surrounding line (or a hit's leading
// window) does not, contextOff stops all further context, so the windows
// already emitted stay intact and the remaining budget goes to bare matches.
type grepOutputBudget struct {
	maxHits    int // <= 0 means unlimited
	maxBytes   int // <= 0 means unlimited
	bytes      int
	lines      int
	hits       int
	contextOff bool
}

func (b *grepOutputBudget) cost(lineLen int) int {
	if b.lines > 0 {
		return lineLen + 1
	}
	return lineLen
}

func (b *grepOutputBudget) fits(cost int) bool {
	return b.maxBytes <= 0 || b.bytes+cost <= b.maxBytes
}

// windowFits reports whether a hit fits together with its leadingCount
// leading lines of leadingBytes formatted bytes.
func (b *grepOutputBudget) windowFits(leadingBytes, leadingCount, hitLen int) bool {
	return b.fits(b.cost(leadingBytes) + leadingCount + hitLen)
}

func (b *grepOutputBudget) add(lineLen int) {
	b.bytes += b.cost(lineLen)
	b.lines++
}

func (b *grepOutputBudget) full() bool {
	return b.maxBytes > 0 && b.bytes >= b.maxBytes
}

func (b *grepOutputBudget) hitCapReached() bool {
	return b.maxHits > 0 && b.hits >= b.maxHits
}

// grepMatchLine formats hits as path:line:text and excerpts as | path-line-text.
// The explicit marker prevents excerpt contents from masquerading as hits.
func grepMatchLine(displayPath string, m grepLineMatch) string {
	sep := ":"
	if m.isContext() {
		sep = "-"
	}
	line := displayPath + sep + strconv.Itoa(m.num) + sep + m.text
	if m.isContext() {
		line = GrepContextLinePrefix + line
	}
	return line
}

// GrepContextLinePrefix distinguishes excerpts from hits even when file text
// contains a path:line:text fragment. Paths that begin with it are quoted.
const GrepContextLinePrefix = "| "

// ParseGrepOutputLine classifies the output without guessing from file text.
func ParseGrepOutputLine(line string) (path string, isContext, ok bool) {
	if strings.HasPrefix(line, GrepContextLinePrefix) {
		return "", true, true
	}
	path, _, _, ok = ParseGrepMatchLine(line)
	return path, false, ok
}

// grepCutAtHitBoundary truncates lines to at most max entries, backing the cut
// up to the last hit so no kept context line is severed from the hit it was
// emitted for. Entries are classified with ParseGrepOutputLine; a line it
// cannot parse stops the walk, because only context lines can be orphaned.
func grepCutAtHitBoundary(lines []string, max int) []string {
	if len(lines) <= max {
		return lines
	}
	cut := max
	for cut > 0 {
		_, isContext, ok := ParseGrepOutputLine(lines[cut-1])
		if !ok || !isContext {
			break
		}
		cut--
	}
	return lines[:cut]
}

// ParseGrepMatchLine parses a match for both display and request reduction.
// Quoted paths escape ambiguous separators and whitespace; surrounding lines
// are never evidence of a match.
func ParseGrepMatchLine(line string) (path, lineNo, snippet string, ok bool) {
	if strings.HasPrefix(line, GrepContextLinePrefix) {
		return "", "", "", false
	}
	rest := line
	if strings.HasPrefix(line, "\"") {
		quoted, err := strconv.QuotedPrefix(line)
		if err != nil {
			return "", "", "", false
		}
		path, err = strconv.Unquote(quoted)
		if err != nil || path == "" {
			return "", "", "", false
		}
		rest = line[len(quoted):]
	} else {
		at := grepSeparatorPairIndex(line, ':')
		if at < 1 {
			return "", "", "", false
		}
		path, rest = line[:at], line[at:]
	}
	if len(rest) < 3 || rest[0] != ':' {
		return "", "", "", false
	}
	end := 1
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 1 || end >= len(rest) || rest[end] != ':' {
		return "", "", "", false
	}
	return path, rest[1:end], rest[end+1:], true
}

func grepDisplayPath(path string) string {
	if strings.HasPrefix(path, GrepContextLinePrefix) || strings.HasPrefix(path, "\"") ||
		strings.ContainsAny(path, " \t\n\r") || grepSeparatorPairIndex(path, ':') >= 0 {
		return strconv.Quote(path)
	}
	return path
}

// grepSeparatorPairIndex returns the index of the first sep that follows a
// non-empty prefix and encloses a run of digits (sep digits sep), or -1.
func grepSeparatorPairIndex(line string, sep byte) int {
	for i := 1; i < len(line); i++ {
		if line[i] != sep {
			continue
		}
		j := i + 1
		for j < len(line) && line[j] >= '0' && line[j] <= '9' {
			j++
		}
		if j > i+1 && j < len(line) && line[j] == sep {
			return i
		}
	}
	return -1
}

func grepMatchLineLen(displayPath string, m grepLineMatch) int {
	digits := 1
	for n := m.num; n >= 10; n /= 10 {
		digits++
	}
	size := len(displayPath) + 1 + digits + 1 + len(m.text)
	if m.isContext() {
		size += len(GrepContextLinePrefix)
	}
	return size
}

// scanGrepFile reads a file and collects matching lines in
// "path:linenum:content" component form, plus up to contextLines surrounding
// lines per hit when requested. Binary files yield an empty scan with no
// error (they still count as scanned). Lines are matched as bytes, so
// non-matching lines allocate nothing while contextLines is 0, and only lines
// that are actually emitted as context are converted when it is positive.
// capMatches/capBytes bound the scan under the same grepOutputBudget rules
// appendBudgetedGrepMatches applies, so when the caps equal the caller's
// remaining output budget the merge keeps the scan's output unchanged.
func scanGrepFile(ctx context.Context, path, baseDir string, re *regexp.Regexp, capMatches, capBytes, contextLines int) grepFileScan {
	if err := ctx.Err(); err != nil {
		return grepFileScan{err: err}
	}
	f, err := os.Open(path)
	if err != nil {
		return grepFileScan{err: err}
	}
	defer f.Close()

	bufs := grepScanBufPool.Get().(*grepScanBuffers)
	defer grepScanBufPool.Put(bufs)

	// Peek the head of the file to detect binary content (NUL bytes, high
	// ratio of control bytes, known binary content-types). Matches ripgrep's
	// default behavior of skipping binary files.
	n, _ := io.ReadFull(f, bufs.head)
	if looksBinary(bufs.head[:n]) {
		return grepFileScan{}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return grepFileScan{err: err}
	}

	scan := grepFileScan{}
	budget := grepOutputBudget{maxHits: capMatches, maxBytes: capBytes, contextOff: contextLines <= 0}
	var ring grepContextRing
	if contextLines > 0 {
		ring = newGrepContextRing(bufs, contextLines)
	}
	omitContext := func() {
		budget.contextOff = true
		scan.contextOmitted = true
		scan.contextCut = len(scan.matches)
	}
	scanner := bufio.NewScanner(f)
	// Reuse the pooled initial buffer; long lines may still grow up to 1 MiB.
	scanner.Buffer(bufs.scan[:0], 1024*1024)
	lineNum := 0
	// trailing counts how many following lines still belong to the window of
	// the hit just emitted. Those lines are emitted directly and never enter
	// the ring, which is reset at every hit; that is what merges adjacent
	// windows: a line a previous hit already emitted as trailing context is
	// never emitted again as the next hit's leading context. finishing is set
	// once the hit cap is reached: the last hit's trailing window is still
	// completed, then the scan stops.
	trailing := 0
	finishing := false

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			scan.err = err
			scan.matches = nil
			scan.displayPath = ""
			return scan
		}
		lineNum++
		line := scanner.Bytes()
		if re.Match(line) {
			if finishing {
				return scan
			}
			if scan.displayPath == "" {
				scan.displayPath = displayPathForBaseDir(path, baseDir)
				if strings.TrimSpace(scan.displayPath) == "" {
					scan.displayPath = path
				}
				scan.displayPath = grepDisplayPath(scan.displayPath)
			}
			hit := grepLineMatch{num: lineNum, text: sanitizeGrepLine(string(line))}
			hitLen := grepMatchLineLen(scan.displayPath, hit)
			if !budget.contextOff && ring.n > 0 {
				mark := len(scan.matches)
				var leadingBytes int
				scan.matches, leadingBytes = ring.appendTo(scan.matches, scan.displayPath)
				if budget.windowFits(leadingBytes, len(scan.matches)-mark, hitLen) {
					for _, m := range scan.matches[mark:] {
						budget.add(grepMatchLineLen(scan.displayPath, m))
					}
				} else {
					scan.matches = scan.matches[:mark]
					omitContext()
				}
			}
			ring.reset()
			scan.matches = append(scan.matches, hit)
			if !budget.fits(budget.cost(hitLen)) {
				// Keep the overflowing match so the budget layer can apply
				// the first-match head-truncation rule, then stop.
				scan.hitCaps = true
				return scan
			}
			budget.add(hitLen)
			budget.hits++
			if budget.full() {
				scan.hitCaps = true
				return scan
			}
			if budget.hitCapReached() {
				scan.hitCaps = true
				if budget.contextOff {
					return scan
				}
				finishing = true
			}
			trailing = contextLines
			continue
		}
		if budget.contextOff {
			if finishing {
				return scan
			}
			continue
		}
		if trailing == 0 {
			if finishing {
				return scan
			}
			ring.push(lineNum, line)
			continue
		}
		trailing--
		entry := grepLineMatch{num: lineNum, text: grepContextText(line), kind: grepLineTrailing}
		entryLen := grepMatchLineLen(scan.displayPath, entry)
		if !budget.fits(budget.cost(entryLen)) {
			omitContext()
			if finishing {
				return scan
			}
			continue
		}
		scan.matches = append(scan.matches, entry)
		budget.add(entryLen)
	}
	scan.err = scanner.Err()
	if scan.err != nil {
		scan.matches = nil
		scan.displayPath = ""
	}
	return scan
}

// grepAppendResult reports what appendBudgetedGrepMatches consumed: hits and
// bytes (file-internal separators included), whether matches were cut by the
// budgets, and whether context is off from here on. contextOff is sticky
// across files: the caller passes it back in for the next file so the output
// never resumes context after omitting it.
type grepAppendResult struct {
	hits       int
	bytes      int
	truncated  bool
	contextOff bool
}

// appendBudgetedGrepMatches formats scan's lines onto dst under the remaining
// match-count and byte budgets with the grepOutputBudget rules: a separator
// byte per additional line within the file, leading context admitted only
// together with its hit, context switched off at the first line that does not
// fit, the overflowing match dropped when earlier file lines exist, and the
// file's first line head-truncated with "..." when that match alone
// overflows the byte budget.
func appendBudgetedGrepMatches(dst []string, scan grepFileScan, remainingMatches, remainingBytes int, contextOff bool) ([]string, grepAppendResult) {
	budget := grepOutputBudget{maxHits: remainingMatches, maxBytes: remainingBytes, contextOff: contextOff}
	result := func(truncated bool) grepAppendResult {
		return grepAppendResult{hits: budget.hits, bytes: budget.bytes, truncated: truncated, contextOff: budget.contextOff}
	}
	// finishing mirrors the scan: after the hit cap, only the last hit's
	// trailing window is still emitted.
	finishing := false
	leadingStart := -1
	for i, m := range scan.matches {
		if scan.contextOmitted && i == scan.contextCut {
			budget.contextOff = true
		}
		switch m.kind {
		case grepLineLeading:
			if finishing {
				return dst, result(true)
			}
			if leadingStart < 0 {
				leadingStart = i
			}
			continue
		case grepLineTrailing:
			if budget.contextOff {
				if finishing {
					return dst, result(true)
				}
				continue
			}
			lineLen := grepMatchLineLen(scan.displayPath, m)
			if !budget.fits(budget.cost(lineLen)) {
				budget.contextOff = true
				if finishing {
					return dst, result(true)
				}
				continue
			}
			dst = append(dst, grepMatchLine(scan.displayPath, m))
			budget.add(lineLen)
			continue
		}
		if finishing {
			return dst, result(true)
		}
		hitLen := grepMatchLineLen(scan.displayPath, m)
		if leadingStart >= 0 {
			leading := scan.matches[leadingStart:i]
			leadingStart = -1
			if !budget.contextOff {
				leadingBytes := 0
				for _, l := range leading {
					leadingBytes += grepMatchLineLen(scan.displayPath, l)
				}
				if budget.windowFits(leadingBytes, len(leading), hitLen) {
					for _, l := range leading {
						dst = append(dst, grepMatchLine(scan.displayPath, l))
						budget.add(grepMatchLineLen(scan.displayPath, l))
					}
				} else {
					budget.contextOff = true
				}
			}
		}
		if !budget.fits(budget.cost(hitLen)) {
			if budget.lines > 0 {
				return dst, result(true)
			}
			prefix := scan.displayPath + ":" + strconv.Itoa(m.num) + ":"
			available := remainingBytes - len(prefix) - len("...")
			if available <= 0 {
				return dst, result(true)
			}
			formatted := prefix + truncateStringToValidUTF8Prefix(m.text, available) + "..."
			if len(formatted) > remainingBytes {
				return dst, result(true)
			}
			budget.add(len(formatted))
			budget.hits++
			return append(dst, formatted), result(true)
		}
		dst = append(dst, grepMatchLine(scan.displayPath, m))
		budget.add(hitLen)
		budget.hits++
		if budget.full() {
			return dst, result(true)
		}
		if budget.hitCapReached() {
			if budget.contextOff {
				return dst, result(true)
			}
			finishing = true
		}
	}
	if scan.contextOmitted && scan.contextCut >= len(scan.matches) {
		budget.contextOff = true
	}
	return dst, result(finishing)
}

// sanitizeGrepLine strips C0 control characters (except tab) and replaces
// invalid UTF-8 byte sequences with U+FFFD. This prevents embedded ESC/CSI
// bytes from corrupting the terminal's SGR state when the result is rendered
// in the TUI, and avoids dumping arbitrary binary bytes into the context.
func sanitizeGrepLine(s string) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// matchIncludePattern supports simple glob patterns including brace expansion
// like "*.{go,ts}".
func matchIncludePattern(name string, pattern string) (bool, error) {
	// Handle brace expansion for patterns like "*.{go,ts}".
	if strings.Contains(pattern, "{") && strings.Contains(pattern, "}") {
		start := strings.Index(pattern, "{")
		end := strings.Index(pattern, "}")
		if start < end {
			prefix := pattern[:start]
			suffix := pattern[end+1:]
			alternatives := strings.SplitSeq(pattern[start+1:end], ",")
			for alt := range alternatives {
				expanded := prefix + strings.TrimSpace(alt) + suffix
				matched, err := filepath.Match(expanded, name)
				if err != nil {
					return false, err
				}
				if matched {
					return true, nil
				}
			}
			return false, nil
		}
	}

	return filepath.Match(pattern, name)
}

func matchAnyIncludePattern(path string, patterns []string) (bool, error) {
	base := filepath.Base(path)
	for _, pattern := range patterns {
		if strings.Contains(pattern, "/") || strings.Contains(pattern, "**") {
			matched, err := doublestar.PathMatch(pattern, path)
			if err != nil || matched {
				return matched, err
			}
			continue
		}
		matched, err := matchIncludePattern(base, pattern)
		if err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}
