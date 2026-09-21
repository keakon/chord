package tools

import (
	"regexp"
	"strings"
)

// shellReadonlyCmdSpec is one generic-table entry: the readonly flag
// sets split by value arity, the write/non-terminating blacklists, and the
// operand ceiling (-1 means unbounded, 0 means no operands). Unknown flags
// veto: a new writing option in a future tool version fails closed.
type shellReadonlyCmdSpec struct {
	ShortNoValue map[byte]bool
	ShortValued  map[byte]bool
	LongNoValue  map[string]bool
	LongValued   map[string]bool
	DenyShort    map[byte]bool
	DenyLong     map[string]bool
	MaxOperands  int
}

func (s *shellReadonlyCmdSpec) denyLong(name string) bool {
	if s == nil {
		return false
	}
	if s.DenyLong[name] {
		return true
	}
	// tail --follow[=name|descriptor]: any --follow* form vetoes, mirroring
	// the old HasPrefix(f, "--follow") check.
	if strings.HasPrefix(name, "follow") {
		for denied := range s.DenyLong {
			if denied == "follow" {
				return true
			}
		}
	}
	return false
}

func newShellSpec(shortNoValue, shortValued string, longNoValue, longValued []string, denyShort string, denyLong []string, maxOperands int) *shellReadonlyCmdSpec {
	spec := &shellReadonlyCmdSpec{
		ShortNoValue: make(map[byte]bool),
		ShortValued:  make(map[byte]bool),
		LongNoValue:  make(map[string]bool),
		LongValued:   make(map[string]bool),
		DenyShort:    make(map[byte]bool),
		DenyLong:     make(map[string]bool),
		MaxOperands:  maxOperands,
	}
	for i := 0; i < len(shortNoValue); i++ {
		spec.ShortNoValue[shortNoValue[i]] = true
	}
	for i := 0; i < len(shortValued); i++ {
		spec.ShortValued[shortValued[i]] = true
	}
	for _, name := range longNoValue {
		spec.LongNoValue[name] = true
	}
	for _, name := range longValued {
		spec.LongValued[name] = true
	}
	for i := 0; i < len(denyShort); i++ {
		spec.DenyShort[denyShort[i]] = true
	}
	for _, name := range denyLong {
		spec.DenyLong[name] = true
	}
	return spec
}

// shellReadonlyGenericTable covers the commands whose only decision is their
// flag table. find, sed, git, and gh need custom handlers and live outside
// this map; command and nice are wrappers handled in the dispatcher.
var shellReadonlyGenericTable = map[string]*shellReadonlyCmdSpec{
	"ls":        newShellSpec("aAlhRdtSrSiuUXFG", "", []string{"all", "almost-all", "human-readable", "reverse", "recursive", "directory", "inode"}, nil, "", nil, -1),
	"cat":       newShellSpec("nbsETv", "", []string{"number-nonblank", "number", "squeeze-blank", "show-ends", "show-tabs", "show-nonprinting"}, nil, "", nil, -1),
	"head":      newShellSpec("qv", "nc", []string{"quiet", "verbose"}, []string{"lines", "bytes"}, "", nil, -1),
	"tail":      newShellSpec("qv", "nc", []string{"quiet", "verbose"}, []string{"lines", "bytes"}, "fF", []string{"follow"}, -1),
	"wc":        newShellSpec("lwcmL", "", []string{"lines", "words", "bytes", "chars", "max-line-length"}, nil, "", nil, -1),
	"stat":      newShellSpec("L", "c", []string{"dereference"}, []string{"format", "printf"}, "", nil, -1),
	"file":      newShellSpec("bzi", "m", []string{"brief", "mime-type", "mime-encoding", "mime"}, []string{"magic-file"}, "C", []string{"compile"}, -1),
	"du":        newShellSpec("shac", "", []string{"summarize", "human-readable", "all"}, nil, "", nil, -1),
	"df":        newShellSpec("h", "", []string{"human-readable"}, nil, "", nil, -1),
	"pwd":       newShellSpec("LP", "", []string{"logical", "physical"}, nil, "", nil, 0),
	"which":     newShellSpec("a", "", []string{"all"}, nil, "", nil, -1),
	"printenv":  newShellSpec("", "", nil, nil, "", nil, -1),
	"diff":      newShellSpec("rqsu", "", []string{"recursive", "unified", "quiet", "brief"}, nil, "", nil, -1),
	"jq":        newShellSpec("rce", "", []string{"raw-output", "compact-output", "exit-status"}, nil, "", nil, -1),
	"rg":        newShellSpec("nivlc", "mg", []string{"line-number", "ignore-case", "invert-match", "files-with-matches", "count", "no-config"}, []string{"glob", "iglob", "max-count"}, "", []string{"pre"}, -1),
	"grep":      newShellSpec("rnRiVlc", "", []string{"recursive", "line-number", "ignore-case", "invert-match", "files-with-matches", "count"}, nil, "", nil, -1),
	"sort":      newShellSpec("rnuh", "k", []string{"reverse", "numeric-sort", "unique", "human-numeric-sort"}, []string{"key"}, "o", []string{"output"}, -1),
	"uniq":      newShellSpec("cduDi", "fsw", []string{"count", "repeated", "all-repeated", "unique", "ignore-case"}, []string{"skip-fields", "skip-chars", "check-chars"}, "", nil, 1),
	"nl":        newShellSpec("", "bn", nil, []string{"body-numbering", "number-format"}, "", nil, -1),
	"comm":      newShellSpec("", "", nil, nil, "", nil, -1),
	"join":      newShellSpec("i", "", []string{"ignore-case"}, nil, "", nil, -1),
	"paste":     newShellSpec("s", "d", []string{"serial"}, []string{"delimiters"}, "", nil, -1),
	"column":    newShellSpec("t", "s", []string{"table"}, []string{"separator"}, "", nil, -1),
	"tree":      newShellSpec("ad", "L", []string{"all", "dirs-only"}, []string{"level"}, "o", []string{"output"}, -1),
	"realpath":  newShellSpec("e", "", []string{"canonicalize"}, nil, "", nil, -1),
	"dirname":   newShellSpec("z", "", []string{"zero"}, nil, "", nil, -1),
	"basename":  newShellSpec("za", "", []string{"zero", "multiple"}, nil, "", nil, -1),
	"readlink":  newShellSpec("f", "", []string{"canonicalize"}, nil, "", nil, -1),
	"shasum":    newShellSpec("", "a", nil, []string{"algorithm"}, "", nil, -1),
	"sha256sum": newShellSpec("", "", nil, nil, "", nil, -1),
	"cut":       newShellSpec("", "dfc", nil, []string{"delimiter", "fields", "characters"}, "", nil, -1),
	"tr":        newShellSpec("dsc", "", []string{"delete", "squeeze-repeats", "complement"}, nil, "", nil, -1),
}

func lookupShellReadonlySpec(name string) *shellReadonlyCmdSpec {
	return shellReadonlyGenericTable[name]
}

// classifyShellWrapperCommand unwraps the "command" builtin. Only the
// bare form continues; any wrapper flag fails closed.
func classifyShellWrapperCommand(args []shellWordValue) ShellReadOnlyVerdict {
	for _, w := range args {
		if !w.Static {
			return ShellReadOnlyVerdict{Reason: "wrapper_unreadable"}
		}
	}
	if len(args) == 0 {
		return ShellReadOnlyVerdict{Reason: "wrapper_empty"}
	}
	if args[0].Value == "--" {
		if len(args) < 2 {
			return ShellReadOnlyVerdict{Reason: "wrapper_empty"}
		}
		return classifyShellWords(args[1].Value, args[2:])
	}
	if strings.HasPrefix(args[0].Value, "-") {
		return ShellReadOnlyVerdict{Reason: "wrapper_flag"}
	}
	if strings.Contains(args[0].Value, "/") {
		return ShellReadOnlyVerdict{Reason: "slash_cmd"}
	}
	if args[0].Value == "" {
		return ShellReadOnlyVerdict{Reason: "empty_cmd"}
	}
	return classifyShellWords(args[0].Value, args[1:])
}

// classifyShellWrapperNice unwraps "nice", stripping an optional
// "-n adjustment" or "-NUM" adjustment before delegating.
func classifyShellWrapperNice(args []shellWordValue) ShellReadOnlyVerdict {
	for _, w := range args {
		if !w.Static {
			return ShellReadOnlyVerdict{Reason: "wrapper_unreadable"}
		}
	}
	if len(args) == 0 {
		return ShellReadOnlyVerdict{Reason: "wrapper_empty"}
	}
	rest := args
	switch {
	case rest[0].Value == "-n":
		if len(rest) < 3 {
			return ShellReadOnlyVerdict{Reason: "wrapper_value_missing"}
		}
		rest = rest[2:]
	case rest[0].Value == "--":
		rest = rest[1:]
		if len(rest) == 0 {
			return ShellReadOnlyVerdict{Reason: "wrapper_empty"}
		}
	case isNumericDash(rest[0].Value):
		rest = rest[1:]
		if len(rest) == 0 {
			return ShellReadOnlyVerdict{Reason: "wrapper_empty"}
		}
	case strings.HasPrefix(rest[0].Value, "-"):
		return ShellReadOnlyVerdict{Reason: "wrapper_flag"}
	}
	if len(rest) == 0 {
		return ShellReadOnlyVerdict{Reason: "wrapper_empty"}
	}
	if strings.Contains(rest[0].Value, "/") {
		return ShellReadOnlyVerdict{Reason: "slash_cmd"}
	}
	if rest[0].Value == "" {
		return ShellReadOnlyVerdict{Reason: "empty_cmd"}
	}
	return classifyShellWords(rest[0].Value, rest[1:])
}

var gitQuerySpec = newShellSpec("abprsv", "n", []string{
	"oneline", "stat", "short", "staged", "cached", "show-current",
	"all", "branches", "tags", "remotes", "graph", "decorate",
	"name-only", "name-status", "patch", "verbose",
}, []string{
	"pretty", "format", "max-count", "since", "until", "author", "grep",
}, "", []string{"output"}, -1)

// gitBranchSpec accepts only listing shapes. A bare branch name creates the
// ref (`git branch new`, `git branch -v new`), and -l/--list take a pattern
// operand in the same position, so both stay out: a glob pattern is also
// unreadable to the classifier. Valued flags such as --contains take a
// commit, not a branch to create. -n is not a branch flag (git rejects it);
// leaving it valued would hide the name as a max-count.
var gitBranchSpec = newShellSpec("arsv", "", []string{
	"all", "remotes", "verbose", "show-current", "color", "no-color",
}, []string{
	"contains", "no-contains", "merged", "no-merged", "points-at",
	"format", "abbrev",
}, "", nil, 0)

var gitConfigSpec = newShellSpec("", "", []string{"get", "get-all", "list"}, nil, "", nil, -1)

var gitTagSpec = newShellSpec("l", "", []string{"list"}, nil, "", nil, -1)

var gitRemoteSpec = newShellSpec("v", "", []string{"verbose"}, nil, "", nil, 0)

var ghSpec = newShellSpec("", "", nil, []string{"limit", "state"}, "", nil, -1)

// classifyGitCall implements the git entry: the subcommand is locked to
// the second word, so global options (-c, --exec-path, -C, ...) veto by
// missing the table. Only the listed query shapes continue.
func classifyGitCall(args []shellWordValue) ShellReadOnlyVerdict {
	for _, w := range args {
		if !w.Static {
			return ShellReadOnlyVerdict{Reason: "git_unreadable"}
		}
	}
	if len(args) == 0 {
		return ShellReadOnlyVerdict{Reason: "git_empty"}
	}
	sub := args[0].Value
	switch sub {
	case "log", "diff", "status", "show", "rev-parse",
		"describe", "blame", "shortlog", "ls-files", "cat-file":
		if ok, reason := parseShellReadonlyFlags(args[1:], gitQuerySpec); !ok {
			return ShellReadOnlyVerdict{Reason: "git:" + reason}
		}
		return ShellReadOnlyVerdict{ReadOnly: true}
	case "branch":
		// A branch name, with or without -v/--verbose, creates the ref.
		// Listings (`git branch`, `git branch -a`, `git branch --show-current`)
		// have no such operand and stay read-only.
		if ok, reason := parseShellReadonlyFlags(args[1:], gitBranchSpec); !ok {
			return ShellReadOnlyVerdict{Reason: "git:" + reason}
		}
		return ShellReadOnlyVerdict{ReadOnly: true}
	case "config":
		if ok, reason := parseShellReadonlyFlags(args[1:], gitConfigSpec); !ok {
			return ShellReadOnlyVerdict{Reason: "git:" + reason}
		}
		if !hasGitFlag(args[1:], "get", "get-all", "list") {
			return ShellReadOnlyVerdict{Reason: "git_config_write"}
		}
		return ShellReadOnlyVerdict{ReadOnly: true}
	case "remote":
		if ok, reason := parseShellReadonlyFlags(args[1:], gitRemoteSpec); !ok {
			return ShellReadOnlyVerdict{Reason: "git:" + reason}
		}
		if !hasGitFlag(args[1:], "v", "verbose") {
			return ShellReadOnlyVerdict{Reason: "git_remote_write"}
		}
		return ShellReadOnlyVerdict{ReadOnly: true}
	case "tag":
		if ok, reason := parseShellReadonlyFlags(args[1:], gitTagSpec); !ok {
			return ShellReadOnlyVerdict{Reason: "git:" + reason}
		}
		if !hasGitFlag(args[1:], "l", "list") {
			return ShellReadOnlyVerdict{Reason: "git_tag_write"}
		}
		return ShellReadOnlyVerdict{ReadOnly: true}
	case "stash":
		if len(args) < 2 || args[1].Value != "list" {
			return ShellReadOnlyVerdict{Reason: "git_stash_write"}
		}
		if ok, reason := parseShellReadonlyFlags(args[2:], gitQuerySpec); !ok {
			return ShellReadOnlyVerdict{Reason: "git:" + reason}
		}
		return ShellReadOnlyVerdict{ReadOnly: true}
	default:
		return ShellReadOnlyVerdict{Reason: "git_subcommand:" + sub}
	}
}

// hasGitFlag reports whether the raw post-subcommand words name one of the
// given flags (short char or long name), used to require --get / -v / -l style
// gates that separate a query shape from a same-subcommand write.
func hasGitFlag(args []shellWordValue, names ...string) bool {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	seenDashDash := false
	for _, arg := range args {
		s := arg.Value
		if seenDashDash {
			continue
		}
		if s == "--" {
			seenDashDash = true
			continue
		}
		if strings.HasPrefix(s, "--") {
			name, _, _ := splitLongFlag(s)
			if want[name] {
				return true
			}
			continue
		}
		if strings.HasPrefix(s, "-") && len(s) > 1 && !isNumericDash(s) && s != "-" {
			cluster := s[1:]
			for j := range len(cluster) {
				if want[string(rune(cluster[j]))] {
					return true
				}
				// A valued short consumes the rest; the gate flags here
				// are all non-valued, so stop at the first valued char to
				// avoid misreading its value as flags.
				if gitQuerySpec.ShortValued[cluster[j]] || gitConfigSpec.ShortValued[cluster[j]] {
					break
				}
			}
		}
	}
	return false
}

// classifyGhCall implements the gh two-level table: pr view|diff|list,
// issue view|list, run view|list.
func classifyGhCall(args []shellWordValue) ShellReadOnlyVerdict {
	for _, w := range args {
		if !w.Static {
			return ShellReadOnlyVerdict{Reason: "gh_unreadable"}
		}
	}
	if len(args) < 2 {
		return ShellReadOnlyVerdict{Reason: "gh_arity"}
	}
	first, second := args[0].Value, args[1].Value
	allowed := false
	switch first {
	case "pr":
		allowed = second == "view" || second == "diff" || second == "list"
	case "issue":
		allowed = second == "view" || second == "list"
	case "run":
		allowed = second == "view" || second == "list"
	}
	if !allowed {
		return ShellReadOnlyVerdict{Reason: "gh_subcommand"}
	}
	if ok, reason := parseShellReadonlyFlags(args[2:], ghSpec); !ok {
		return ShellReadOnlyVerdict{Reason: "gh:" + reason}
	}
	return ShellReadOnlyVerdict{ReadOnly: true}
}

var sedPrintScript = regexp.MustCompile(`^[0-9,$]*p$`)

// classifySedCall implements the narrow sed subset: -n plus a single
// print script matching ^[0-9,$]*p$, with the script word quoted
// (Unquoted:false) so that "1w f" and "s/a/b/e" cannot slip through.
func classifySedCall(args []shellWordValue) ShellReadOnlyVerdict {
	for _, w := range args {
		if !w.Static {
			return ShellReadOnlyVerdict{Reason: "sed_unreadable"}
		}
	}
	hasN := false
	seenDashDash := false
	var operands []shellWordValue
	for _, w := range args {
		s := w.Value
		if seenDashDash {
			operands = append(operands, w)
			continue
		}
		if s == "--" {
			seenDashDash = true
			continue
		}
		if s == "-" {
			operands = append(operands, w)
			continue
		}
		if s == "-n" || s == "--quiet" || s == "--silent" {
			hasN = true
			continue
		}
		if strings.HasPrefix(s, "-") {
			return ShellReadOnlyVerdict{Reason: "sed_flag"}
		}
		operands = append(operands, w)
	}
	if !hasN {
		return ShellReadOnlyVerdict{Reason: "sed_no_n"}
	}
	if len(operands) == 0 {
		return ShellReadOnlyVerdict{Reason: "sed_no_script"}
	}
	script := operands[0]
	if script.Unquoted {
		return ShellReadOnlyVerdict{Reason: "sed_script_unquoted"}
	}
	if !sedPrintScript.MatchString(script.Value) {
		return ShellReadOnlyVerdict{Reason: "sed_script"}
	}
	return ShellReadOnlyVerdict{ReadOnly: true}
}

var findValuedPredicates = map[string]bool{
	"name": true, "iname": true, "type": true, "maxdepth": true, "mindepth": true,
}

var findBarePredicates = map[string]bool{
	"print": true, "print0": true, "ls": true, "prune": true,
	"and": true, "or": true, "not": true, "true": true, "false": true,
}

var findDenyPredicates = map[string]bool{
	"delete": true, "exec": true, "execdir": true, "ok": true, "okdir": true,
	"fls": true, "fprint": true, "fprint0": true, "fprintf": true,
}

// classifyFindCall implements the find expression gate: readonly predicates
// continue, write predicates (-delete, -exec, -ok, -fprint, -fls, ...) veto,
// and unknown predicates fail closed.
func classifyFindCall(args []shellWordValue) ShellReadOnlyVerdict {
	for _, w := range args {
		if !w.Static {
			return ShellReadOnlyVerdict{Reason: "find_unreadable"}
		}
	}
	i := 0
	for i < len(args) {
		s := args[i].Value
		if s == "--" {
			// Not an option terminator here: GNU find consumes a leading
			// "--" as the end of options and then parses what follows as
			// paths plus the expression (find -- . -delete deletes), while
			// a "--" after a path is an unknown predicate. Neither shape is
			// provably read-only.
			return ShellReadOnlyVerdict{Reason: "find_dash_dash"}
		}
		if s == "!" || s == "(" || s == ")" {
			i++
			continue
		}
		if s == "-" {
			i++
			continue
		}
		if name, ok := strings.CutPrefix(s, "-"); ok {
			name, _ = strings.CutPrefix(name, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
			}
			if findDenyPredicates[name] || hasFindDenyPrefix(name) {
				return ShellReadOnlyVerdict{Reason: "find_deny:" + name}
			}
			if findValuedPredicates[name] {
				if i+1 >= len(args) {
					return ShellReadOnlyVerdict{Reason: "find_value_missing:" + name}
				}
				i += 2
				continue
			}
			if findBarePredicates[name] {
				i++
				continue
			}
			return ShellReadOnlyVerdict{Reason: "find_unknown:" + name}
		}
		// Path operand.
		i++
	}
	return ShellReadOnlyVerdict{ReadOnly: true}
}

func hasFindDenyPrefix(name string) bool {
	for denied := range findDenyPredicates {
		if strings.HasPrefix(name, denied) && name != denied {
			// -execdir vs -exec, -fprint0 vs -fprint: any extension of a
			// denied stem stays denied.
			return true
		}
	}
	return false
}
