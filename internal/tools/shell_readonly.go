package tools

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/keakon/golog/log"
	"mvdan.cc/sh/v3/syntax"
)

// ShellReadOnlyVerdict is the outcome of the read-only classifier. ReadOnly
// is true only when the command is provably free of writes, process side
// effects, and non-terminating behavior. Reason names the first veto for
// debug logs; it is empty when ReadOnly is true.
type ShellReadOnlyVerdict struct {
	ReadOnly bool
	Reason   string
}

// ClassifyShellReadOnly reports whether a shell invocation is provably
// read-only. It is a pure function: no I/O, no file reads, no cwd dependence
// beyond the shellType variant. Any uncertainty returns false.
//
// runInBackground always vetoes: starting a detached job is a process side
// effect larger than a file write. powershell always vetoes: the classifier
// only models POSIX/bash word semantics.
func ClassifyShellReadOnly(command, shellType string, runInBackground bool) ShellReadOnlyVerdict {
	if runInBackground {
		return ShellReadOnlyVerdict{Reason: "run_in_background"}
	}
	if strings.TrimSpace(shellType) == "powershell" {
		return ShellReadOnlyVerdict{Reason: "powershell"}
	}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ShellReadOnlyVerdict{Reason: "empty"}
	}
	if cached, ok := shellReadOnlyCacheLoad(shellType, trimmed); ok {
		return cached
	}
	verdict := classifyShellReadOnlyUncached(trimmed, shellType)
	log.Debugf("shell readonly verdict command=%q shell=%s readonly=%v reason=%s", trimmed, shellType, verdict.ReadOnly, verdict.Reason)
	shellReadOnlyCacheStore(shellType, trimmed, verdict)
	return verdict
}

// shellReadOnlyArgsVerdict adapts the classifier to tool JSON args plus the
// ShellTool instance's shell type. JSON or command extraction failures are
// fail-closed.
func shellReadOnlyArgsVerdict(args json.RawMessage, shellType string) ShellReadOnlyVerdict {
	var parsed struct {
		Command         string `json:"command"`
		RunInBackground bool   `json:"run_in_background"`
	}
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return ShellReadOnlyVerdict{Reason: "bad_args"}
	}
	return ClassifyShellReadOnly(parsed.Command, shellType, parsed.RunInBackground)
}

func classifyShellReadOnlyUncached(command, shellType string) ShellReadOnlyVerdict {
	variant := shellLangVariant(shellType)
	parser := syntax.NewParser(syntax.Variant(variant))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return ShellReadOnlyVerdict{Reason: "parse_error"}
	}
	return classifyShellFile(file)
}

// shellLangVariant follows the shell the command will actually run under:
// posix parses as LangPOSIX, everything else (bash, git-bash, empty for
// tests, unknown) parses as LangBash. powershell never reaches here.
func shellLangVariant(shellType string) syntax.LangVariant {
	if strings.TrimSpace(shellType) == "posix" {
		return syntax.LangPOSIX
	}
	return syntax.LangBash
}

// shellWordValue is the classifier's value primitive. Static means the exact value
// was recovered; Unquoted means it came (at least partly) from an unquoted
// *Lit and may have undergone glob expansion. Static:false is "unreadable,
// veto the whole command" and must never be confused with a legal empty
// string (Static:true, Value:"").
type shellWordValue struct {
	Value    string
	Static   bool
	Unquoted bool
}

// shellUnexpandedLitChars lists the characters that make an unquoted literal
// word differ from the argv the command receives: quote removal strips a
// backslash, pathname expansion rewrites * ? [, and brace expansion rewrites
// { }. Each can forge a flag from an innocent-looking operand: a file named
// "-o" turns "sort *" into "sort -o ...".
const shellUnexpandedLitChars = "\\*?[{}"

// evalShellWordValue recovers a word's exact value without using Word.Lit,
// which returns "" for quoted literals and truncates LiteralArgs.
func evalShellWordValue(word *syntax.Word) shellWordValue {
	if word == nil || len(word.Parts) == 0 {
		return shellWordValue{Static: false}
	}
	var sb strings.Builder
	unquoted := false
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(p.Value, shellUnexpandedLitChars) {
				// The shell rewrites these before the command sees the
				// word, and the parser reports every one of them as a
				// plain *Lit (BraceExp only appears after an explicit
				// SplitBraces expansion the classifier never runs), so an
				// unquoted word carrying any of them is unreadable. The
				// same characters inside quotes never expand and stay
				// literal payload in their own cases.
				return shellWordValue{Static: false}
			}
			sb.WriteString(p.Value)
			unquoted = true
		case *syntax.SglQuoted:
			if p.Dollar {
				// $'...' decodes ANSI-C escapes before exec: $'\x2do'
				// reaches the command as -o.
				return shellWordValue{Static: false}
			}
			sb.WriteString(p.Value)
			// Quoted: Static stays, Unquoted untouched.
		case *syntax.DblQuoted:
			inner := evalDblQuotedValue(p)
			if !inner.Static {
				return shellWordValue{Static: false}
			}
			sb.WriteString(inner.Value)
			// Double-quoted content never marks the word unquoted, even
			// when the inner pieces are literals.
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst, *syntax.ExtGlob:
			return shellWordValue{Static: false}
		default:
			return shellWordValue{Static: false}
		}
	}
	return shellWordValue{Value: sb.String(), Static: true, Unquoted: unquoted}
}

func evalDblQuotedValue(q *syntax.DblQuoted) shellWordValue {
	if q == nil {
		return shellWordValue{Static: false}
	}
	var sb strings.Builder
	for _, part := range q.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return shellWordValue{Static: false}
		}
		sb.WriteString(lit.Value)
	}
	return shellWordValue{Value: sb.String(), Static: true}
}

// evalCallWordValues converts a CallExpr's Args to value primitives in order.
func evalCallWordValues(call *syntax.CallExpr) []shellWordValue {
	if call == nil {
		return nil
	}
	out := make([]shellWordValue, 0, len(call.Args))
	for _, w := range call.Args {
		out = append(out, evalShellWordValue(w))
	}
	return out
}

func classifyShellFile(file *syntax.File) ShellReadOnlyVerdict {
	if file == nil || len(file.Stmts) == 0 {
		return ShellReadOnlyVerdict{Reason: "no_stmts"}
	}
	for _, stmt := range file.Stmts {
		if v := classifyShellStmt(stmt); !v.ReadOnly {
			return v
		}
	}
	return ShellReadOnlyVerdict{ReadOnly: true}
}

// classifyShellStmt enforces the closed statement set plus the redirect
// layer. Only *CallExpr and &&/||/| *BinaryCmd continue; every other Command
// implementation (including future ones via default) vetoes.
func classifyShellStmt(stmt *syntax.Stmt) ShellReadOnlyVerdict {
	if stmt == nil {
		return ShellReadOnlyVerdict{Reason: "nil_stmt"}
	}
	if stmt.Background {
		return ShellReadOnlyVerdict{Reason: "background"}
	}
	if stmt.Coprocess {
		return ShellReadOnlyVerdict{Reason: "coprocess"}
	}
	if stmt.Disown {
		return ShellReadOnlyVerdict{Reason: "disown"}
	}
	if stmt.Negated {
		return ShellReadOnlyVerdict{Reason: "negated"}
	}
	for _, r := range stmt.Redirs {
		if ok, reason := shellRedirectAllowed(r); !ok {
			return ShellReadOnlyVerdict{Reason: reason}
		}
	}
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		return classifyShellCallExpr(cmd)
	case *syntax.BinaryCmd:
		switch cmd.Op {
		case syntax.AndStmt, syntax.OrStmt, syntax.Pipe:
			// Allowed operators: both sides must be read-only.
		default:
			return ShellReadOnlyVerdict{Reason: "binary_op"}
		}
		if cmd.X == nil || cmd.Y == nil {
			return ShellReadOnlyVerdict{Reason: "binary_nil"}
		}
		if v := classifyShellStmt(cmd.X); !v.ReadOnly {
			return v
		}
		if v := classifyShellStmt(cmd.Y); !v.ReadOnly {
			return v
		}
		return ShellReadOnlyVerdict{ReadOnly: true}
	default:
		return ShellReadOnlyVerdict{Reason: "structure"}
	}
}

// shellRedirectAllowed judges one Stmt redirect. Only pure-input
// redirections pass, and >& / <& pass solely for numeric or "-" targets so
// that "2>&1" stays read-only while ">& file" does not.
func shellRedirectAllowed(r *syntax.Redirect) (bool, string) {
	if r == nil {
		return false, "nil_redir"
	}
	switch r.Op {
	case syntax.RdrIn, syntax.WordHdoc:
		// Pure input.
	case syntax.DplIn, syntax.DplOut:
		target := evalShellWordValue(r.Word)
		if !target.Static {
			return false, "redir_target_unreadable"
		}
		if target.Value == "-" || isDigits(target.Value) {
			break
		}
		return false, "redir_dup_target"
	default:
		return false, "redir_op"
	}
	// A process substitution hiding in the redirect target (sort < <(rm))
	// must veto even when the operator itself is innocent.
	if r.Word != nil {
		if target := evalShellWordValue(r.Word); !target.Static {
			return false, "redir_target_unreadable"
		}
	}
	return true, ""
}

// classifyShellCallExpr rejects any assignment prefix and dispatches
// the command word to the wrapper / custom / generic tables.
func classifyShellCallExpr(call *syntax.CallExpr) ShellReadOnlyVerdict {
	if call == nil || len(call.Args) == 0 {
		return ShellReadOnlyVerdict{Reason: "no_args"}
	}
	if len(call.Assigns) > 0 {
		return ShellReadOnlyVerdict{Reason: "assign"}
	}
	words := evalCallWordValues(call)
	if len(words) == 0 || !words[0].Static {
		return ShellReadOnlyVerdict{Reason: "cmd_unreadable"}
	}
	name := words[0].Value
	if name == "" {
		return ShellReadOnlyVerdict{Reason: "empty_cmd"}
	}
	if strings.Contains(name, "/") {
		return ShellReadOnlyVerdict{Reason: "slash_cmd"}
	}
	return classifyShellWords(name, words[1:])
}

// classifyShellWords is the command-layer dispatcher shared by top-level
// calls and unwrapped wrapper payloads.
func classifyShellWords(name string, args []shellWordValue) ShellReadOnlyVerdict {
	if name == "command" {
		return classifyShellWrapperCommand(args)
	}
	if name == "nice" {
		return classifyShellWrapperNice(args)
	}
	if name == "git" {
		return classifyGitCall(args)
	}
	if name == "gh" {
		return classifyGhCall(args)
	}
	if name == "sed" {
		return classifySedCall(args)
	}
	if name == "find" {
		return classifyFindCall(args)
	}
	spec := lookupShellReadonlySpec(name)
	if spec == nil {
		return ShellReadOnlyVerdict{Reason: "unknown_cmd:" + name}
	}
	if ok, reason := parseShellReadonlyFlags(args, spec); !ok {
		return ShellReadOnlyVerdict{Reason: reason}
	}
	if name == "rg" && !hasGitFlag(args, "no-config") {
		// rg reads RIPGREP_CONFIG_PATH unless --no-config is set, and that
		// file can add --pre, which runs an external command. A bare rg is
		// therefore not provably read-only.
		return ShellReadOnlyVerdict{Reason: "rg_config"}
	}
	return ShellReadOnlyVerdict{ReadOnly: true}
}

// parseShellReadonlyFlags implements the shared flag rules: "--" terminates
// flag parsing, short clusters expand with whole-cluster blacklist checks,
// valued options consume either the cluster remainder or the next word, and
// any unknown flag vetoes.
func parseShellReadonlyFlags(args []shellWordValue, spec *shellReadonlyCmdSpec) (bool, string) {
	for _, w := range args {
		if !w.Static {
			return false, "unreadable_arg"
		}
	}
	operands := 0
	seenDashDash := false
	i := 0
	for i < len(args) {
		s := args[i].Value
		if seenDashDash {
			operands++
			i++
			continue
		}
		if s == "--" {
			seenDashDash = true
			i++
			continue
		}
		if s == "-" {
			operands++
			i++
			continue
		}
		if isNumericDash(s) {
			// "-20" style counts (head, git log): readonly, not an operand.
			i++
			continue
		}
		if strings.HasPrefix(s, "--") {
			name, value, hasEq := splitLongFlag(s)
			if name == "" {
				return false, "bad_long"
			}
			if spec.denyLong(name) {
				return false, "deny_long:" + name
			}
			if spec.LongNoValue[name] {
				if hasEq {
					return false, "long_value_unexpected:" + name
				}
				i++
				continue
			}
			if spec.LongValued[name] {
				if hasEq {
					_ = value
					i++
					continue
				}
				if i+1 >= len(args) {
					return false, "long_value_missing:" + name
				}
				// Next word is the value (already known Static); it is not
				// an operand.
				i += 2
				continue
			}
			return false, "unknown_long:" + name
		}
		if strings.HasPrefix(s, "-") && len(s) > 1 {
			cluster := s[1:]
			consumedValueWord := false
			clusterOK := true
			var clusterReason string
			for j := 0; j < len(cluster); j++ {
				c := cluster[j]
				if spec.DenyShort[c] {
					clusterOK = false
					clusterReason = "deny_short:" + string(rune(c))
					break
				}
				if spec.ShortNoValue[c] {
					continue
				}
				if spec.ShortValued[c] {
					if j+1 < len(cluster) {
						// Remainder of the cluster is the value
						// (tail -n99); the f in -fn99 already vetoed above.
					} else if i+1 < len(args) {
						consumedValueWord = true
					} else {
						clusterOK = false
						clusterReason = "short_value_missing:" + string(rune(c))
						break
					}
					break
				}
				clusterOK = false
				clusterReason = "unknown_short:" + string(rune(c))
				break
			}
			if !clusterOK {
				return false, clusterReason
			}
			i++
			if consumedValueWord {
				i++
			}
			continue
		}
		operands++
		i++
	}
	if spec.MaxOperands >= 0 && operands > spec.MaxOperands {
		return false, "arity"
	}
	return true, ""
}

func splitLongFlag(s string) (name, value string, hasEq bool) {
	rest := strings.TrimPrefix(s, "--")
	name, value, ok := strings.Cut(rest, "=")
	if !ok {
		return rest, "", false
	}
	return name, value, true
}

func isNumericDash(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	return isDigits(s[1:])
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// shellReadOnlyCache memoizes classifier verdicts: the classifier is pure over
// (command, shellType), while the streaming speculative path and the batch
// builder re-evaluate the same prefix repeatedly. A background invocation is
// vetoed before it can reach the cache, so run_in_background is not part of
// the key. The cache is bounded so a long-lived process cannot grow it without
// limit: once full, new verdicts are still computed correctly, they just are
// not retained.
const maxShellReadOnlyCacheEntries = 2048

var shellReadOnlyCache = struct {
	sync.Mutex
	verdicts map[string]ShellReadOnlyVerdict
}{verdicts: make(map[string]ShellReadOnlyVerdict)}

func shellReadOnlyCacheKey(shellType, command string) string {
	var sb strings.Builder
	sb.WriteString(shellType)
	sb.WriteByte(0)
	sb.WriteString(command)
	return sb.String()
}

func shellReadOnlyCacheLoad(shellType, command string) (ShellReadOnlyVerdict, bool) {
	shellReadOnlyCache.Lock()
	defer shellReadOnlyCache.Unlock()
	verdict, ok := shellReadOnlyCache.verdicts[shellReadOnlyCacheKey(shellType, command)]
	return verdict, ok
}

func shellReadOnlyCacheStore(shellType, command string, verdict ShellReadOnlyVerdict) {
	shellReadOnlyCache.Lock()
	defer shellReadOnlyCache.Unlock()
	if len(shellReadOnlyCache.verdicts) >= maxShellReadOnlyCacheEntries {
		return
	}
	shellReadOnlyCache.verdicts[shellReadOnlyCacheKey(shellType, command)] = verdict
}
