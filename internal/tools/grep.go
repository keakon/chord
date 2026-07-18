package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// GrepTool searches file contents using a regex pattern.
type GrepTool struct {
	BaseDir string // session working directory for relative paths; empty keeps process cwd behavior
}

type grepArgs struct {
	Pattern         string   `json:"pattern"`
	Paths           []string `json:"paths,omitempty"`
	Includes        []string `json:"includes,omitempty"`
	PathsCoerced    bool     `json:"-"`
	IncludesCoerced bool     `json:"-"`
}

// UnmarshalJSON accepts either a string or array of strings for paths and
// includes, recording whether a scalar was coerced into a single-element list.
// This keeps strict array semantics in the documented schema while preventing
// hard failures when models supply a single string by habit.
func (a *grepArgs) UnmarshalJSON(data []byte) error {
	var raw struct {
		Pattern  string          `json:"pattern"`
		Paths    json.RawMessage `json:"paths,omitempty"`
		Includes json.RawMessage `json:"includes,omitempty"`
		Path     json.RawMessage `json:"path,omitempty"`
		Glob     json.RawMessage `json:"glob,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	a.Pattern = raw.Pattern
	// Accept deprecated singular fields when their current counterparts are
	// absent so legacy-shaped calls still work; current fields always win.
	if len(raw.Paths) == 0 {
		raw.Paths = raw.Path
	}
	if len(raw.Includes) == 0 {
		raw.Includes = raw.Glob
	}
	paths, pathsCoerced, err := DecodeStringOrList(raw.Paths)
	if err != nil {
		return fmt.Errorf("paths: %w", err)
	}
	includes, includesCoerced, err := DecodeStringOrList(raw.Includes)
	if err != nil {
		return fmt.Errorf("includes: %w", err)
	}
	a.Paths = paths
	a.Includes = includes
	a.PathsCoerced = pathsCoerced
	a.IncludesCoerced = includesCoerced
	return nil
}

const (
	maxGrepMatches     = 120
	maxGrepOutputBytes = 12 * 1024
)

func (GrepTool) Name() string { return NameGrep }

func (t GrepTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameGrep, pathsToolConcurrencyPolicyInDir(args, "paths", t.BaseDir))
}

func (GrepTool) Description() string {
	return "Search file contents using a regular expression. If pattern is not valid regex, it is safely searched as literal text and the result reports that fallback." +
		" Use paths for one or more files/directories (JSON array, e.g. paths: [\"internal\", \"cmd\"]), and includes for optional path globs such as **/*.go (JSON array, e.g. includes: [\"**/*.go\"]). Relative paths resolve from the session working directory." +
		" If the exact file path is known, pass the full file path in paths instead of searching its parent directory with the filename in includes; includes filters files during traversal and does not avoid walking the search path." +
		" Single bare strings are tolerated for paths/includes but arrays are preferred." +
		" Returns matching lines with file paths and line numbers." +
		" Best for discovering candidate files, symbols, or text matches when the exact location is not known yet." +
		" For semantic navigation at a known position (definition, references, implementations), prefer the lsp tool when the file type has LSP coverage."
}

func (GrepTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regular expression for file contents. If invalid as regex, it is searched as literal text and the result reports that fallback.",
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
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
}

func (GrepTool) IsReadOnly() bool { return true }

func (GrepTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (GrepTool) CanRenderBeforeToolUseEnd(json.RawMessage) bool { return true }

// legacyArgAliases maps deprecated singular field names to the current plural
// schema fields so legacy-shaped calls validate without exposing the old names
// in Parameters().
func (GrepTool) legacyArgAliases() map[string]string {
	return map[string]string{"path": "paths", "glob": "includes"}
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

	re, err := regexp.Compile(a.Pattern)
	literalFallback := false
	if err != nil {
		re = regexp.MustCompile(regexp.QuoteMeta(a.Pattern))
		literalFallback = true
	}

	var matches []string
	var outputBytes int
	var scannedFiles int64
	truncated := false
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
		rootMatches, rootBytes, rootScanned, rootTruncated, err := grepSearchRoot(ctx, searchPath, resolvedSearchPath, info, re, includes, t.BaseDir, maxGrepMatches-len(matches), maxGrepOutputBytes-outputBytes)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			pathErrors = append(pathErrors, fmt.Sprintf("%s: %v", resolvedSearchPath, err))
			continue
		}
		matches = append(matches, rootMatches...)
		outputBytes += rootBytes
		scannedFiles += rootScanned
		if rootTruncated || len(matches) >= maxGrepMatches || outputBytes >= maxGrepOutputBytes {
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
	// Append per-path failures as notes when partial results exist.
	for _, pe := range pathErrors {
		notes = append(notes, "grep: skipped path: "+pe)
	}
	if len(matches) == 0 {
		logSlowSearch("Grep", searchLabel, a.Pattern, filter, startedAt, "scanned_files", int(scannedFiles), 0, truncated)
		msg := "No matches found."
		if literalFallback {
			msg = "No matches found. (pattern was invalid regex; searched as literal text)"
		}
		msg += " If the symbol or phrase is expected, try alternate naming, a narrower literal, or broaden the search scope (paths/includes) before assuming absence."
		return prependNotes(notes, msg), nil
	}

	if len(matches) > maxGrepMatches {
		matches = matches[:maxGrepMatches]
	}

	result := strings.Join(matches, "\n")
	if literalFallback {
		result = "Note: pattern was invalid regex; searched as literal text.\n" + result
	}
	result = prependNotes(notes, result)
	if truncated || len(matches) == maxGrepMatches || len(result) >= maxGrepOutputBytes {
		result += fmt.Sprintf("\n\n(showing first %d matches within %d KiB; narrow paths/includes/pattern for more precise results)", len(matches), maxGrepOutputBytes/1024)
	}
	logSlowSearch("Grep", searchLabel, a.Pattern, filter, startedAt, "scanned_files", int(scannedFiles), len(matches), truncated)
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
		notes = append(notes, "Note: paths was a single string; treated as one path. Prefer paths: [...] next time.")
	}
	if a.IncludesCoerced {
		notes = append(notes, "Note: includes was a single string; treated as one filter. Prefer includes: [...] next time.")
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

func grepSearchRoot(ctx context.Context, searchPath, resolvedSearchPath string, info os.FileInfo, re *regexp.Regexp, includes []string, baseDir string, maxMatches, maxBytes int) ([]string, int, int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, false, err
	}
	if maxMatches <= 0 || maxBytes <= 0 {
		return nil, 0, 0, true, nil
	}
	if !info.IsDir() {
		if err := ensureRegularFilePath(searchPath, info); err != nil {
			return nil, 0, 0, false, err
		}
		if IsBinaryExtension(filepath.Base(resolvedSearchPath)) {
			return nil, 0, 1, false, nil
		}
		scan := scanGrepFile(ctx, resolvedSearchPath, baseDir, re, maxMatches, maxBytes)
		if scan.err != nil {
			return nil, 0, 0, false, scan.err
		}
		matches, bytesUsed, truncated := appendBudgetedGrepMatches(nil, scan, maxMatches, maxBytes)
		reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: 1})
		return matches, bytesUsed, 1, truncated, nil
	}

	// Fast path: when includes contains a relative path with no glob metacharacters
	// (e.g. "architecture-review-20260621-064007.html" or "src/main.go"), try the
	// exact file under the search root before recursively walking it. This avoids
	// walking huge roots (like the system temp directory) when the caller already
	// knows the relative path.
	if exactFiles, ok := resolveExactIncludeFiles(resolvedSearchPath, includes); ok {
		var matches []string
		var outputBytes int
		var scannedFiles int64
		truncated := false
		for _, file := range exactFiles {
			if err := ctx.Err(); err != nil {
				return nil, 0, scannedFiles, truncated, err
			}
			remainingMatches := maxMatches - len(matches)
			remainingBytes := maxBytes - outputBytes
			if remainingMatches <= 0 || remainingBytes <= 0 {
				truncated = true
				break
			}
			info, err := os.Stat(file)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if IsBinaryExtension(filepath.Base(file)) {
				continue
			}
			scan := scanGrepFile(ctx, file, baseDir, re, remainingMatches, remainingBytes)
			if scan.err != nil {
				return nil, 0, 0, false, scan.err
			}
			prevLen := len(matches)
			var bytesUsed int
			var fileTruncated bool
			matches, bytesUsed, fileTruncated = appendBudgetedGrepMatches(matches, scan, remainingMatches, remainingBytes)
			if prevLen > 0 && len(matches) > prevLen {
				outputBytes++
			}
			outputBytes += bytesUsed
			scannedFiles++
			if fileTruncated {
				truncated = true
				break
			}
		}
		if scannedFiles > 0 {
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
		}
		return matches, outputBytes, scannedFiles, truncated, nil
	}

	return grepWalkRoot(ctx, resolvedSearchPath, re, includes, baseDir, maxMatches, maxBytes)
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
	n := runtime.GOMAXPROCS(0)
	if n < 1 {
		n = 1
	}
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
func grepWalkRoot(ctx context.Context, resolvedSearchPath string, re *regexp.Regexp, includes []string, baseDir string, maxMatches, maxBytes int) ([]string, int, int64, bool, error) {
	return grepWalkRootWithScanner(ctx, resolvedSearchPath, re, includes, baseDir, maxMatches, maxBytes, scanGrepFile)
}

func grepWalkRootWithScanner(ctx context.Context, resolvedSearchPath string, re *regexp.Regexp, includes []string, baseDir string, maxMatches, maxBytes int, scanFile grepFileScanner) ([]string, int, int64, bool, error) {
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
		wg.Add(1)
		go func() {
			defer wg.Done()
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
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var matches []string
	var outputBytes int
	var scannedFiles int64
	truncated := false
	budgetDone := false

	process := func(scan grepFileScan) {
		if budgetDone || scan.err != nil {
			return
		}
		remainingMatches := maxMatches - len(matches)
		remainingBytes := maxBytes - outputBytes
		if remainingMatches <= 0 || remainingBytes <= 0 {
			truncated = true
			budgetDone = true
			cancel()
			return
		}
		prevLen := len(matches)
		var bytesUsed int
		var fileTruncated bool
		matches, bytesUsed, fileTruncated = appendBudgetedGrepMatches(matches, scan, remainingMatches, remainingBytes)
		if prevLen > 0 && len(matches) > prevLen {
			outputBytes++
		}
		outputBytes += bytesUsed
		scannedFiles++
		if scannedFiles <= 5 || scannedFiles%10 == 0 {
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
		}
		if fileTruncated || len(matches) >= maxMatches || outputBytes >= maxBytes {
			truncated = true
			budgetDone = true
			cancel()
		}
	}

	pending := make(map[int]grepFileScan)
	next := 0
	for res := range results {
		pending[res.idx] = res.scan
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
	if err := parentCtx.Err(); err != nil {
		return nil, 0, scannedFiles, truncated, err
	}
	switch {
	case walkErr == nil:
	case errors.Is(walkErr, errGrepWalkCanceled):
	case errors.Is(walkErr, errGuardAbort):
		return nil, 0, scannedFiles, truncated, guard.abortError()
	default:
		return nil, 0, scannedFiles, truncated, fmt.Errorf("walking directory: %w", walkErr)
	}
	if scannedFiles > 0 {
		reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
	}
	return matches, outputBytes, scannedFiles, truncated, nil
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

// grepLineMatch is one matching line from a scanned file, kept as components
// so budget application can reformat (and truncate) it exactly like the
// former inline formatting did.
type grepLineMatch struct {
	num  int
	text string // sanitized display text
}

// grepFileScan is the outcome of scanning one file: the display path, the
// matching lines in file order, whether the scan stopped at its caps, and any
// open/read error. A scan that stopped at caps includes the first match that
// overflowed the byte cap so the budget layer can apply the same
// head-truncation rule a direct scan would.
type grepFileScan struct {
	displayPath string
	matches     []grepLineMatch
	hitCaps     bool
	err         error
}

// grepScanBuffers holds per-scan reusable allocations: the binary-detection
// head sample and the line scanner's initial buffer.
type grepScanBuffers struct {
	head []byte
	scan []byte
}

var grepScanBufPool = sync.Pool{
	New: func() any {
		return &grepScanBuffers{
			head: make([]byte, binarySampleBytes),
			scan: make([]byte, 0, 64*1024),
		}
	},
}

func grepMatchLine(displayPath string, num int, text string) string {
	return displayPath + ":" + strconv.Itoa(num) + ":" + text
}

func grepMatchLineLen(displayPath string, num int, text string) int {
	digits := 1
	for n := num; n >= 10; n /= 10 {
		digits++
	}
	return len(displayPath) + 1 + digits + 1 + len(text)
}

// scanGrepFile reads a file and collects matching lines in
// "path:linenum:content" component form. Binary files yield an empty scan
// with no error (they still count as scanned). Lines are matched as bytes so
// non-matching lines allocate nothing. capMatches/capBytes bound the scan
// exactly like the former searchFile budget accounting; when the caps equal
// the caller's remaining output budget, appendBudgetedGrepMatches reproduces
// the former output byte for byte.
func scanGrepFile(ctx context.Context, path, baseDir string, re *regexp.Regexp, capMatches, capBytes int) grepFileScan {
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
	outputBytes := 0
	scanner := bufio.NewScanner(f)
	// Reuse the pooled initial buffer; long lines may still grow up to 1 MiB.
	scanner.Buffer(bufs.scan[:0], 1024*1024)
	lineNum := 0

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			scan.err = err
			scan.matches = nil
			scan.displayPath = ""
			return scan
		}
		lineNum++
		if !re.Match(scanner.Bytes()) {
			continue
		}
		if scan.displayPath == "" {
			scan.displayPath = displayPathForBaseDir(path, baseDir)
			if strings.TrimSpace(scan.displayPath) == "" {
				scan.displayPath = path
			}
		}
		text := sanitizeGrepLine(string(scanner.Bytes()))
		matchBytes := grepMatchLineLen(scan.displayPath, lineNum, text)
		if len(scan.matches) > 0 {
			matchBytes++
		}
		if capBytes > 0 && outputBytes+matchBytes > capBytes {
			// Include the overflowing match so the budget layer can apply the
			// same first-match head-truncation rule, then stop scanning.
			scan.matches = append(scan.matches, grepLineMatch{num: lineNum, text: text})
			scan.hitCaps = true
			return scan
		}
		scan.matches = append(scan.matches, grepLineMatch{num: lineNum, text: text})
		outputBytes += matchBytes
		if capMatches > 0 && len(scan.matches) >= capMatches {
			scan.hitCaps = true
			return scan
		}
		if capBytes > 0 && outputBytes >= capBytes {
			scan.hitCaps = true
			return scan
		}
	}
	scan.err = scanner.Err()
	if scan.err != nil {
		scan.matches = nil
		scan.displayPath = ""
	}
	return scan
}

// appendBudgetedGrepMatches formats scan's matches onto dst while enforcing
// the remaining match-count and byte budgets, mirroring the former searchFile
// accounting: a separator byte per additional match within the file, drop the
// overflowing match when earlier file matches exist, and head-truncate with
// "..." when the file's first match alone overflows the byte budget. It
// returns the extended slice, the bytes consumed (file-internal separators
// included), and whether output was truncated against the budgets.
func appendBudgetedGrepMatches(dst []string, scan grepFileScan, remainingMatches, remainingBytes int) ([]string, int, bool) {
	bytesUsed := 0
	appended := 0
	for _, m := range scan.matches {
		formatted := grepMatchLine(scan.displayPath, m.num, m.text)
		matchBytes := len(formatted)
		if appended > 0 {
			matchBytes++
		}
		if remainingBytes > 0 && bytesUsed+matchBytes > remainingBytes {
			if appended > 0 {
				return dst, bytesUsed, true
			}
			prefix := scan.displayPath + ":" + strconv.Itoa(m.num) + ":"
			available := remainingBytes - len(prefix) - len("...")
			if available <= 0 {
				return dst, bytesUsed, true
			}
			formatted = prefix + truncateStringToValidUTF8Prefix(m.text, available) + "..."
			if len(formatted) > remainingBytes {
				return dst, bytesUsed, true
			}
			return append(dst, formatted), bytesUsed + len(formatted), true
		}
		dst = append(dst, formatted)
		appended++
		bytesUsed += matchBytes
		if remainingMatches > 0 && appended >= remainingMatches {
			return dst, bytesUsed, true
		}
		if remainingBytes > 0 && bytesUsed >= remainingBytes {
			return dst, bytesUsed, true
		}
	}
	return dst, bytesUsed, false
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
