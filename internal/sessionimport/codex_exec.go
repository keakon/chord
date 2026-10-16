package sessionimport

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Codex "code mode" contract.
//
// Verified against the Codex CLI source (codex-rs/code-mode-protocol and
// codex-rs/core/src/tools/code_mode). Codex exposes one freeform top-level tool:
//
//   - `exec` accepts raw JavaScript source text — not JSON, not a quoted string,
//     not a markdown fence. A first-line pragma `// @exec: {"yield_time_ms": N,
//     "max_output_tokens": N}` may precede the script.
//   - Nested tools hang off the global `tools` object and are called as
//     `await tools.<identifier>(argument)`. Function-shaped nested tools take an
//     argument object (`exec_command`, `write_stdin`); freeform-shaped ones take a
//     string (`apply_patch`). Names are normalized to JavaScript identifiers, so
//     an MCP tool `mcp__server__tool` stays dotted as written.
//   - The isolate evaluates the script once: it may issue zero, one, or many
//     nested calls, and helpers such as `store`/`load`/`yield_control`/`text`
//     carry state between calls.
//
// One `exec` record therefore says nothing on its own about which Chord tool ran.
// Only a script that provably issues exactly one nested call of a known shape can
// be mapped; every other script stays the source `exec` call under its own
// arguments. parseCodexExecScript owns that judgement. Both direction of error
// are closed: a mapped script must have run its nested call exactly once, and
// an unmapped script never loses the call it recorded.

// codexExecPragmaPrefix is Codex's optional first-line `exec` pragma.
const codexExecPragmaPrefix = "// @exec:"

// codexConstDeclPrefix is the only mutable-proof declaration form whose string
// binding the importer resolves. let and var are rejected because reassignment
// would make the recorded value wrong.
const codexConstDeclPrefix = "const "

// codexMaxJSSafeInteger matches Codex's MAX_JS_SAFE_INTEGER check on pragma
// values: anything larger, fractional, or in exponent notation is a pragma the
// host refuses to run.
const codexMaxJSSafeInteger = 1<<53 - 1

// Nested tool names this importer understands. Codex exposes far more (MCP
// tools, skills, image helpers); anything else is kept as the source exec call
// rather than guessed at.
const (
	codexNestedExecCommand = "exec_command"
	codexNestedWriteStdin  = "write_stdin"
	codexNestedApplyPatch  = "apply_patch"
)

// codexTextHelper is the Codex code-mode output helper the import templates
// wrap a nested call in (`text(await tools.apply_patch(patch))`). Its argument
// list is the one transparent parenthesis around a call site.
const codexTextHelper = "text"

// codexExecScript is one analyzed `exec` input: the sole nested call it makes,
// with either the function-shaped argument object or the freeform string input.
type codexExecScript struct {
	// Nested is the nested tool identifier exactly as written in the script,
	// for example "exec_command" or "apply_patch".
	Nested string
	// Args is the argument object of a function-shaped nested call.
	Args map[string]any
	// Input is the string argument of a freeform nested call.
	Input string
}

// codexNestedCallSite is one `tools.<name>(...)` occurrence found outside
// string literals and comments. argStart is the raw offset just past the
// opening parenthesis, so binding lookups can compare source positions.
type codexNestedCallSite struct {
	name      string
	argSrc    string
	argStart  int
	siteStart int // offset of "tools." in the raw source
}

// codexExecScan is one pass over an `exec` script: every nested call site it can
// see, plus a skeleton of the source whose string, template, and comment bodies
// are blanked so keyword checks cannot be fooled by a shell command that happens
// to contain "for " or "tools.".
type codexExecScan struct {
	raw      string
	skeleton string
	sites    []codexNestedCallSite
}

// parseCodexExecScript reports the single nested call an `exec` script makes. It
// fails closed: no nested call, more than one call site, a dynamic `tools[...]`
// access, a loop or parallel construct, or an argument shape it cannot evaluate
// all mean "not provably one known call", so the caller keeps the source exec
// call instead of mapping it. A false result never means no work happened — it
// means only that the work cannot be attributed to exactly one nested call.
func parseCodexExecScript(input string) (codexExecScript, bool) {
	code, ok := codexStripExecPragma(input)
	if !ok {
		return codexExecScript{}, false
	}
	scan, ok := codexScanExecScript(code)
	if !ok || len(scan.sites) != 1 {
		return codexExecScript{}, false
	}
	// Helpers and control flow mean the script composes several invocations or
	// defers them, and `store`/`load` carry state into later exec calls, so one
	// record no longer describes one action. Words are matched as whole
	// JavaScript tokens so compact code ("for(") cannot slip a call site through
	// while a substring like "format" stays inert; symbol-bearing forms are
	// matched anywhere. Keywords inside strings and comments are already
	// blanked in the skeleton.
	for _, word := range []string{
		"if", "else", "for", "while", "do", "switch", "try", "catch",
		"function", "setTimeout", "yield_control", "notify", "exit", "ALL_TOOLS",
	} {
		if tokenIsCodeWord(scan.skeleton, word) {
			return codexExecScript{}, false
		}
	}
	for _, fragment := range []string{
		"=>", ".map(", ".forEach(", "Promise.all", "store(", "load(",
	} {
		if strings.Contains(scan.skeleton, fragment) {
			return codexExecScript{}, false
		}
	}

	// A call reachable only inside a conditional expression — after "&&",
	// "||", "?", ":", or "," — cannot be proven to run, and a ternary makes
	// the call's presence depend on runtime state. Any such guard around the
	// single site fails the mapping. The scan runs on the skeleton, where
	// string bodies are blank, so an "await" inside a string never masquerades
	// as the keyword.
	site := scan.sites[0]
	if codexSiteGuarded(scan.skeleton, site.siteStart) {
		return codexExecScript{}, false
	}
	// A site nested in a block, function or class body, or any argument list
	// other than the `text` helper's sits inside a construct whose run count
	// the scan cannot prove. Only a statement at the script's top level, or an
	// argument of a statement-level `text(...)`, is mapped.
	if codexSiteNested(scan.skeleton, site.siteStart) {
		return codexExecScript{}, false
	}
	script := codexExecScript{Nested: site.name}
	argSrc := strings.TrimSpace(site.argSrc)
	if argSrc == "" {
		return codexExecScript{}, false
	}
	if value, ok := parseCodexJSValue(argSrc); ok {
		switch v := value.(type) {
		case string:
			script.Input = v
			return script, true
		case map[string]any:
			script.Args = v
			return script, true
		default:
			return codexExecScript{}, false
		}
	}
	// A freeform nested call may pass a variable bound by a const string
	// declaration earlier in the script, which is how Codex's own apply_patch
	// template is written.
	if bound, ok := codexExecStringBinding(scan, site.argStart, argSrc); ok {
		script.Input = bound
		return script, true
	}
	return codexExecScript{}, false
}

// tokenIsCodeWord reports whether word appears in the skeleton as a whole
// JavaScript token, not as a substring of a longer identifier ("for" inside
// "formatter"). Symbol-bearing fragments are matched with plain Contains by
// the caller instead, because their edge characters are never identifier
// parts.
func tokenIsCodeWord(skeleton, word string) bool {
	for offset := 0; ; {
		idx := strings.Index(skeleton[offset:], word)
		if idx < 0 {
			return false
		}
		idx += offset
		before := idx == 0 || !isJSIdentPart(skeleton[idx-1])
		after := idx+len(word) >= len(skeleton) || !isJSIdentPart(skeleton[idx+len(word)])
		if before && after {
			return true
		}
		offset = idx + len(word)
	}
}

// codexSiteGuarded reports whether the token before the call site (skipping
// whitespace, including line breaks, and `await` keywords) is a conditional
// operator, which means the site sits in an expression the runtime may never
// evaluate. A line break does not end the operator's expression: `flag &&\n
// tools.exec_command(...)` still calls conditionally.
func codexSiteGuarded(skeleton string, siteStart int) bool {
	i := siteStart
	for i > 0 {
		for i > 0 && isJSSpace(skeleton[i-1]) {
			i--
		}
		if i >= 5 && skeleton[i-5:i] == "await" && (i == 5 || !isJSIdentPart(skeleton[i-6])) {
			i -= 5
			continue
		}
		break
	}
	return i > 0 && strings.IndexByte("&|?:,", skeleton[i-1]) >= 0
}

// codexSiteNested reports whether the call site sits inside a construct whose
// execution the single-call proof does not cover: a brace (block, class or
// function body, object literal) or a parenthesis that is not the argument
// list of Codex's `text` output helper. A call evaluates its arguments exactly
// once when it runs, so `text(await tools.apply_patch(patch))` — Codex's own
// apply_patch template — still maps; the wrapper itself must sit at statement
// level, or a conditional such as `flag && text(...)` would guard the nested
// call through it. Depth is counted on the skeleton, where string and comment
// bodies are blank.
func codexSiteNested(skeleton string, siteStart int) bool {
	type openContext struct {
		bracket byte
		start   int  // the wrapper identifier's offset, for the guard check
		textArg bool // the argument list of the `text` helper
	}
	stack := make([]openContext, 0, 4)
	for i := range siteStart {
		switch skeleton[i] {
		case '(':
			context := openContext{bracket: '(', start: i}
			if name, start, ok := codexCallWrapper(skeleton, i); ok && name == codexTextHelper {
				context.start, context.textArg = start, true
			}
			stack = append(stack, context)
		case '{', '[':
			stack = append(stack, openContext{bracket: skeleton[i], start: i})
		case ')', '}', ']':
			if n := len(stack); n > 0 {
				stack = stack[:n-1]
			}
		}
	}
	for _, context := range stack {
		if !context.textArg || codexSiteGuarded(skeleton, context.start) {
			return true
		}
	}
	return false
}

// codexCallWrapper reports the identifier immediately before the parenthesis
// at open, when the parenthesis is a call's argument list: `text` in
// `text(...)`. start is the identifier's offset. A parenthesis whose callee is
// a member access or another expression has no such wrapper.
func codexCallWrapper(skeleton string, open int) (string, int, bool) {
	i := codexSkipJSSpaceBack(skeleton, open)
	if i == 0 || !isJSIdentPart(skeleton[i-1]) {
		return "", 0, false
	}
	start := i - 1
	for start > 0 && isJSIdentPart(skeleton[start-1]) {
		start--
	}
	// A dotted callee (`obj.text(...)`) keeps its head elsewhere, where the
	// guard check below cannot see it, so it does not count as the helper.
	if start > 0 && skeleton[start-1] == '.' {
		return "", 0, false
	}
	return skeleton[start:i], start, true
}

func isJSSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// codexStripExecPragma removes Codex's optional first-line `// @exec: {...}`
// pragma. The pragma is host metadata (yield timing and output budget), never
// part of the script. It is validated exactly the way Codex parses it — a JSON
// object limited to `yield_time_ms` and `max_output_tokens`, both non-negative
// safe integers — because Codex refuses to run the script of an invalid pragma,
// so such a record never executed a nested call and must not be mapped to one.
func codexStripExecPragma(input string) (string, bool) {
	if strings.TrimSpace(input) == "" {
		return "", false
	}
	first, rest, hasRest := strings.Cut(input, "\n")
	if !strings.HasPrefix(strings.TrimLeft(first, " \t"), codexExecPragmaPrefix) {
		return strings.TrimSpace(input), true
	}
	if !hasRest || strings.TrimSpace(rest) == "" {
		return "", false
	}
	directive := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(first), codexExecPragmaPrefix))
	if directive == "" {
		return "", false
	}
	var pragma map[string]json.Number
	if err := json.Unmarshal([]byte(directive), &pragma); err != nil {
		return "", false
	}
	for key, value := range pragma {
		switch key {
		case "yield_time_ms", "max_output_tokens":
			// ParseUint only accepts plain digits, so exponent and fractional
			// forms (`1e3`, `1.5`) are rejected the same way Codex's integer
			// deserialization rejects them. json.Number preserves the raw text.
			number, err := strconv.ParseUint(value.String(), 10, 64)
			if err != nil || number > uint64(codexMaxJSSafeInteger) {
				return "", false
			}
		default:
			return "", false
		}
	}
	return strings.TrimSpace(rest), true
}

// codexScanExecScript walks the script once, tracking JavaScript string,
// template, and comment state. A dynamic `tools[...]` access fails the scan: it
// would hide the nested tool name behind runtime data. Whitespace and comments
// are skipped around the access operator, so `tools . exec_command(...)` names
// the same call as `tools.exec_command(...)` instead of slipping past the walk
// unrecorded.
func codexScanExecScript(code string) (codexExecScan, bool) {
	scan := codexExecScan{raw: code}
	skeleton := strings.Builder{}
	skeleton.Grow(len(code))
	for i := 0; i < len(code); {
		switch {
		case codexHasPrefixAt(code, i, "//"):
			end := strings.IndexByte(code[i:], '\n')
			if end < 0 {
				skeleton.WriteString(strings.Repeat(" ", len(code)-i))
				i = len(code)
				continue
			}
			skeleton.WriteString(strings.Repeat(" ", end))
			i += end
		case codexHasPrefixAt(code, i, "/*"):
			end := strings.Index(code[i+2:], "*/")
			if end < 0 {
				return codexExecScan{}, false
			}
			skeleton.WriteString(strings.Repeat(" ", end+4))
			i += end + 4
		case codexToolsTokenAt(code, i):
			next, ok := codexScanToolsMember(code, i, &scan, &skeleton)
			if !ok {
				return codexExecScan{}, false
			}
			i = next
		case code[i] == '/' && codexRegexStartsAt(code, i):
			// A regex literal is data, not code: its body must not contribute
			// call sites or keywords to the skeleton, and an unterminated one
			// means the script cannot be walked reliably.
			end := codexSkipJSRegex(code, i)
			if end < 0 {
				return codexExecScan{}, false
			}
			skeleton.WriteString(strings.Repeat(" ", end-i))
			i = end
		case code[i] == '\'' || code[i] == '"' || code[i] == '`':
			next := codexSkipJSString(code, i)
			if next < 0 {
				return codexExecScan{}, false
			}
			skeleton.WriteString(strings.Repeat(" ", next-i))
			i = next
		default:
			skeleton.WriteByte(code[i])
			i++
		}
	}
	scan.skeleton = skeleton.String()
	return scan, true
}

func codexPrevByte(s string, i int) byte {
	if i == 0 {
		return 0
	}
	return s[i-1]
}

// codexToolsTokenAt reports whether i starts the `tools` identifier itself, not
// the tail of a longer name such as `notools`.
func codexToolsTokenAt(s string, i int) bool {
	if isJSIdentPart(codexPrevByte(s, i)) || !codexHasPrefixAt(s, i, "tools") {
		return false
	}
	end := i + len("tools")
	return end >= len(s) || !isJSIdentPart(s[end])
}

// codexScanToolsMember consumes the `tools` token at start and the member
// access that follows it, returning the offset to resume the walk at.
//
// JavaScript allows whitespace and comments around the access operator and
// before the argument list, so the access is classified only after skipping
// them: a `[` hides the nested name behind runtime data and fails the scan, a
// `.` names the call, and anything else is the bare identifier passed around as
// a value. Matching the operator byte-for-byte let `tools [name](...)` and
// `tools\n.exec_command(...)` walk past unrecorded, which made a script with
// several calls look like one.
func codexScanToolsMember(s string, start int, scan *codexExecScan, skeleton *strings.Builder) (int, bool) {
	member := codexSkipJSTrivia(s, start+len("tools"))
	if member < 0 {
		return 0, false
	}
	if member >= len(s) || s[member] != '.' {
		if member < len(s) && s[member] == '[' {
			return 0, false
		}
		// The bare identifier: no call site, and any helper it is handed to is
		// already refused by the keyword scan.
		skeleton.WriteString(strings.Repeat(" ", member-start))
		return member, true
	}
	nameStart := codexSkipJSTrivia(s, member+1)
	if nameStart < 0 {
		return 0, false
	}
	end := codexSkipJSIdent(s, nameStart)
	name := s[nameStart:end]
	call := codexSkipJSTrivia(s, end)
	if call < 0 {
		return 0, false
	}
	if name == "" || call >= len(s) || s[call] != '(' {
		// A property access, not a call. Blank it so a keyword scan cannot see
		// a nested name that never ran.
		blank := max(end-start, len("tools"))
		skeleton.WriteString(strings.Repeat(" ", blank))
		return start + blank, true
	}
	argSrc, _, ok := codexExtractParens(s, call)
	if !ok {
		return 0, false
	}
	scan.sites = append(scan.sites, codexNestedCallSite{name: name, argSrc: argSrc, argStart: call + 1, siteStart: start})
	// The argument source stays part of the walk, so a nested call inside it is
	// counted as a second site instead of being missed.
	skeleton.WriteString(codexBlankNonCode(s[start : call+1]))
	return call + 1, true
}

// codexSkipJSTrivia skips whitespace and comments, returning the offset of the
// next code byte, or -1 when a block comment never terminates.
func codexSkipJSTrivia(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r':
			i++
		case codexHasPrefixAt(s, i, "//"):
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				return len(s)
			}
			i += end
		case codexHasPrefixAt(s, i, "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return -1
			}
			i += end + 4
		default:
			return i
		}
	}
	return i
}

// codexBlankNonCode blanks every string and comment body in a fragment, keeping
// its byte length so skeleton offsets still line up with the source.
func codexBlankNonCode(fragment string) string {
	scan, ok := codexScanExecScript(fragment)
	if !ok {
		return strings.Repeat(" ", len(fragment))
	}
	return scan.skeleton
}

func codexHasPrefixAt(s string, i int, prefix string) bool {
	return i+len(prefix) <= len(s) && s[i:i+len(prefix)] == prefix
}

func codexSkipJSIdent(s string, i int) int {
	for i < len(s) && isJSIdentPart(s[i]) {
		i++
	}
	return i
}

func codexSkipJSSpace(s string, i int) int {
	for i < len(s) && isJSSpace(s[i]) {
		i++
	}
	return i
}

func isJSIdentPart(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// codexSkipJSString returns the index just past a JavaScript string literal
// starting at i, or -1 when it never terminates. A template literal with a
// substitution is rejected: its value cannot be known without evaluating the
// script.
func codexSkipJSString(s string, i int) int {
	quote := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case quote:
			return j + 1
		case '\n':
			if quote != '`' {
				return -1
			}
		case '$':
			if quote == '`' && j+1 < len(s) && s[j+1] == '{' {
				return -1
			}
		}
	}
	return -1
}

// codexRegexStartsAt reports whether the slash at i opens a regex literal
// rather than dividing. JavaScript decides lexically: a slash right after a
// value (identifier, closing paren or bracket) divides, one after an operator,
// opening punctuation, or start of input starts a regex. The keyword list
// matters because keywords end in identifier characters, so the plain
// identifier check alone would misread `return /re/` as division and walk the
// regex body as code.
func codexRegexStartsAt(code string, i int) bool {
	prev := i
	for prev > 0 && (code[prev-1] == ' ' || code[prev-1] == '\t' || code[prev-1] == '\n' || code[prev-1] == '\r') {
		prev--
	}
	if prev == 0 {
		return true
	}
	switch code[prev-1] {
	case ')', ']':
		return false
	case '}', '.', ',', ';', ':', '(', '[', '{',
		'=', '+', '-', '*', '%', '&', '|', '^', '~', '!', '<', '>', '?':
		return true
	}
	if !isJSIdentPart(code[prev-1]) {
		return true
	}
	start := prev - 1
	for start > 0 && isJSIdentPart(code[start-1]) {
		start--
	}
	switch code[start:prev] {
	case "return", "typeof", "instanceof", "in", "of", "new", "delete",
		"void", "throw", "case", "do", "else", "yield", "await":
		return true
	}
	return false
}

// codexSkipJSRegex returns the index just past a regex literal starting at i,
// including its flags, or -1 when it never terminates on this line. Regex
// literals cannot span lines, and inside a character class a slash does not
// close the literal.
func codexSkipJSRegex(s string, i int) int {
	inClass := false
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				j++
				for j < len(s) && isJSIdentPart(s[j]) {
					j++
				}
				return j
			}
		case '\n':
			return -1
		}
	}
	return -1
}

// codexExtractParens returns the source between the parentheses starting at open
// and the index just past the closing parenthesis, skipping strings and comments
// so a parenthesis inside a command does not close the call early.
func codexExtractParens(s string, open int) (string, int, bool) {
	depth := 0
	for i := open; i < len(s); {
		switch {
		case codexHasPrefixAt(s, i, "//"):
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				return "", 0, false
			}
			i += end
		case codexHasPrefixAt(s, i, "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return "", 0, false
			}
			i += end + 4
		case s[i] == '\'' || s[i] == '"' || s[i] == '`':
			next := codexSkipJSString(s, i)
			if next < 0 {
				return "", 0, false
			}
			i = next
		case s[i] == '(':
			depth++
			i++
		case s[i] == ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i + 1, true
			}
			i++
		default:
			i++
		}
	}
	return "", 0, false
}

// codexExecStringBinding resolves the `const name = "literal"` binding of a
// freeform nested call's argument. Only a const declaration counts: a let/var
// binding can be reassigned, which would record a value the source never ran.
// The declaration must also still be in scope at the call — no brace between
// declaration and call may close the declaring block — and the variable must
// never be assigned again anywhere in the script. A reassignment inside a
// shadowing block still rejects the binding: the walk cannot prove scopes, so
// it fails closed to keeping the source call instead of guessing a value.
func codexExecStringBinding(scan codexExecScan, argStart int, name string) (string, bool) {
	if !isJSIdentifier(name) || argStart <= 0 || argStart > len(scan.raw) {
		return "", false
	}
	declPrefix := codexConstDeclPrefix + name
	for offset := 0; offset+1 < len(scan.skeleton); {
		idx := strings.Index(scan.skeleton[offset:], declPrefix)
		if idx < 0 {
			break
		}
		idx += offset
		offset = idx + len(declPrefix)
		// A whole declaration, not `xconst patch` or `const patchy`.
		if idx > 0 && isJSIdentPart(scan.skeleton[idx-1]) {
			continue
		}
		if offset < len(scan.skeleton) && isJSIdentPart(scan.skeleton[offset]) {
			continue
		}
		// Declared after the call, so it was not yet initialized there.
		if offset >= argStart {
			break
		}
		raw := strings.TrimLeft(scan.raw[offset:], " \t")
		if !strings.HasPrefix(raw, "=") || len(raw) < 2 || raw[1] == '=' {
			continue
		}
		init := strings.TrimLeft(raw[1:], " \t")
		if init == "" || (init[0] != '\'' && init[0] != '"' && init[0] != '`') {
			continue
		}
		// The declaring block must not close between the declaration and the
		// call, or the binding is out of scope and the call saw something else.
		depth := 0
		visible := true
		for _, c := range scan.skeleton[idx:argStart] {
			switch c {
			case '{':
				depth++
			case '}':
				depth--
			}
			if depth < 0 {
				visible = false
				break
			}
		}
		if !visible {
			continue
		}
		end := codexSkipJSString(init, 0)
		if end < 0 {
			continue
		}
		// The initializer must be exactly one string literal. Anything after it
		// before the statement terminator — a `+`, a second literal (including
		// adjacent-string concatenation), a ternary, or a call — is an
		// expression the proof cannot evaluate, so fail closed and keep the
		// source call rather than binding a partial or wrong value.
		if !codexInitIsLoneString(init[end:]) {
			continue
		}
		value, ok := parseCodexJSValue(init[:end])
		if !ok {
			continue
		}
		literal, ok := value.(string)
		if !ok {
			continue
		}
		if codexNameReassigned(scan, offset, name) {
			return "", false
		}
		return literal, true
	}
	return "", false
}

// codexInitIsLoneString reports whether the tail after a string literal is only
// whitespace, comments, and an optional terminating semicolon — that is, the
// const initializer was a single string literal, not an expression. It returns
// true at end of input too, where no semicolon closes the declaration. Any
// other token (a second string, an operator, an identifier) means the value is
// not a single provable literal and the binding must be rejected.
func codexInitIsLoneString(tail string) bool {
	for i := 0; i < len(tail); {
		c := tail[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case codexHasPrefixAt(tail, i, "//"):
			e := strings.IndexByte(tail[i:], '\n')
			if e < 0 {
				return true
			}
			i += e
		case codexHasPrefixAt(tail, i, "/*"):
			e := strings.Index(tail[i+2:], "*/")
			if e < 0 {
				return true
			}
			i += e + 4
		case c == ';':
			return true
		default:
			return false
		}
	}
	return true
}

// codexNameReassigned reports whether the identifier name is assigned anywhere
// at or after from in the script: `name = x`, any compound form (`+=`, `||=`),
// or `name++`. Reads like passing the variable to a helper do not count.
func codexNameReassigned(scan codexExecScan, from int, name string) bool {
	for offset := from; offset < len(scan.skeleton); {
		idx := strings.Index(scan.skeleton[offset:], name)
		if idx < 0 {
			return false
		}
		idx += offset
		offset = idx + len(name)
		if idx > 0 && isJSIdentPart(scan.skeleton[idx-1]) {
			continue
		}
		if offset < len(scan.skeleton) && isJSIdentPart(scan.skeleton[offset]) {
			continue
		}
		cursor := codexSkipJSSpace(scan.raw, offset)
		// Any assignment operator directly after the name reassigns it: plain
		// `=`, every compound form (`+=`, `||=`, `**=`), and `++`/`--`.
		for _, op := range []string{
			"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "**=",
			"<<=", ">>=", ">>>=", "&&=", "||=", "??=", "++", "--",
		} {
			if strings.HasPrefix(scan.raw[cursor:], op) {
				return true
			}
		}
		if cursor < len(scan.raw) && scan.raw[cursor] == '=' &&
			(cursor+1 >= len(scan.raw) || scan.raw[cursor+1] != '=') {
			return true
		}
		// Prefix increment/decrement (`++patch`) assigns as well.
		before := codexSkipJSSpaceBack(scan.raw, idx)
		if before >= 2 && (scan.raw[before-2:before] == "++" || scan.raw[before-2:before] == "--") {
			return true
		}
	}
	return false
}

func codexSkipJSSpaceBack(s string, i int) int {
	for i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
		i--
	}
	return i
}

func isJSIdentifier(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isJSIdentPart(s[i]) {
			return false
		}
	}
	return true
}

// Codex status lines, from `format_script_status` in
// codex-rs/core/src/tools/code_mode/mod.rs. Every `exec` output is prefixed with
// "{status}\nWall time {t} seconds\nOutput:\n", so the header has to come off
// before the body can stand in for a Chord tool result.
const (
	codexExecStatusCompleted  = "Script completed"
	codexExecStatusFailed     = "Script failed"
	codexExecStatusTerminated = "Script terminated"
	codexExecStatusRunning    = "Script running with cell ID"
)

// codexExecResult is a decoded `exec` result: the script's status and the output
// body with the envelope removed.
type codexExecResult struct {
	Status string
	Output string
}

// succeeded reports whether the script ran to completion. A yielded cell has
// no finished result to import, and a terminated or failed one did not finish
// normally.
func (r codexExecResult) succeeded() bool {
	return r.Status == codexExecStatusCompleted
}

// yielded reports whether the script returned control while its cell keeps
// running. The result carries a cell id, not an outcome: its continuation in
// the source session is a later `wait` call, so an importer must not pose the
// yielded record as a finished result of any kind.
func (r codexExecResult) yielded() bool {
	return strings.HasPrefix(r.Status, codexExecStatusRunning)
}

// parseCodexExecResult splits Codex's `exec` result envelope. It reports false
// for anything that is not that envelope, in which case the caller keeps the
// output verbatim rather than dropping it.
func parseCodexExecResult(output string) (codexExecResult, bool) {
	first, rest, ok := strings.Cut(output, "\n")
	if !ok {
		return codexExecResult{}, false
	}
	status := strings.TrimSpace(first)
	switch {
	case status == codexExecStatusCompleted,
		status == codexExecStatusFailed,
		status == codexExecStatusTerminated,
		strings.HasPrefix(status, codexExecStatusRunning+" "):
	default:
		return codexExecResult{}, false
	}
	// "Wall time 1.2 seconds", optionally with the code-mode overhead breakdown.
	wall, rest, ok := strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(wall, "Wall time ") {
		return codexExecResult{}, false
	}
	body, ok := strings.CutPrefix(rest, "Output:\n")
	if !ok {
		// A script that printed nothing still ends with the bare header.
		body, ok = strings.CutPrefix(rest, "Output:")
		if !ok {
			return codexExecResult{}, false
		}
	}
	return codexExecResult{Status: status, Output: body}, true
}
