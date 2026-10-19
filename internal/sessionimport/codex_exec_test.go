package sessionimport

import (
	"strings"
	"testing"
)

// `exec` inputs shaped like the ones a Codex rollout records. Codex emits raw
// JavaScript, so these fixtures are the contract this parser exists to satisfy:
// if one of them stops parsing, the importer silently loses a tool call. The
// session ids and yield budgets are placeholders, not a collected rollout.
const (
	sampleExecCommandScript = `const r = await tools.exec_command({cmd:"git status --short --branch && git log --oneline @{u}..HEAD","workdir":"/repo","yield_time_ms":2500,"max_output_tokens":8000});
text(r.output);`

	sampleExecCommandEscapedQuotesScript = `const r = await tools.exec_command({cmd:"rg -n \"type Service|\\.Notes|Payload\" src | head -200","workdir":"/repo","yield_time_ms":2500,"max_output_tokens":8000});
text(r.output);`

	sampleWriteStdinScript = `const r = await tools.write_stdin({session_id:481,yield_time_ms:2500,max_output_tokens:8000,chars:""});
text(JSON.stringify(r));`

	sampleApplyPatchScript = `const patch = "*** Begin Patch\n*** Update File: /repo/README.md\n@@\n-old\n+new\n*** End Patch";
text(await tools.apply_patch(patch));`
)

func TestParseCodexExecScript(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    codexExecScript
		wantOK  bool
		checker func(t *testing.T, got codexExecScript)
	}{
		{
			name:   "exec_command",
			input:  sampleExecCommandScript,
			wantOK: true,
			checker: func(t *testing.T, got codexExecScript) {
				t.Helper()
				if got.Nested != codexNestedExecCommand {
					t.Fatalf("Nested=%q", got.Nested)
				}
				if got.Args["cmd"] != "git status --short --branch && git log --oneline @{u}..HEAD" {
					t.Fatalf("cmd=%#v", got.Args["cmd"])
				}
				if got.Args["workdir"] != "/repo" {
					t.Fatalf("workdir=%#v", got.Args["workdir"])
				}
				if got.Args["yield_time_ms"] != float64(2500) || got.Args["max_output_tokens"] != float64(8000) {
					t.Fatalf("numbers=%#v", got.Args)
				}
			},
		},
		{
			name:   "exec_command with escaped quotes",
			input:  sampleExecCommandEscapedQuotesScript,
			wantOK: true,
			checker: func(t *testing.T, got codexExecScript) {
				t.Helper()
				cmd, _ := got.Args["cmd"].(string)
				if !strings.HasPrefix(cmd, `rg -n "type Service|\.Notes|Payload" src`) {
					t.Fatalf("cmd=%q", cmd)
				}
			},
		},
		{
			name:   "write_stdin",
			input:  sampleWriteStdinScript,
			wantOK: true,
			checker: func(t *testing.T, got codexExecScript) {
				t.Helper()
				if got.Nested != codexNestedWriteStdin {
					t.Fatalf("Nested=%q", got.Nested)
				}
				if got.Args["session_id"] != float64(481) || got.Args["chars"] != "" {
					t.Fatalf("Args=%#v", got.Args)
				}
			},
		},
		{
			name:   "apply_patch through a const binding",
			input:  sampleApplyPatchScript,
			wantOK: true,
			checker: func(t *testing.T, got codexExecScript) {
				t.Helper()
				if got.Nested != codexNestedApplyPatch {
					t.Fatalf("Nested=%q", got.Nested)
				}
				if !strings.HasPrefix(got.Input, "*** Begin Patch\n*** Update File: /repo/README.md") || !strings.HasSuffix(got.Input, "*** End Patch") {
					t.Fatalf("Input=%q", got.Input)
				}
				if !strings.Contains(got.Input, "\n-old\n+new\n") {
					t.Fatalf("newlines lost: %q", got.Input)
				}
			},
		},
		{
			name:   "apply_patch with an inline string",
			input:  "await tools.apply_patch(\"*** Begin Patch\\n*** End Patch\");",
			wantOK: true,
			checker: func(t *testing.T, got codexExecScript) {
				t.Helper()
				if got.Input != "*** Begin Patch\n*** End Patch" {
					t.Fatalf("Input=%q", got.Input)
				}
			},
		},
		{
			name:   "single quoted strings and trailing comma",
			input:  "const r = await tools.exec_command({cmd: 'echo \"hi\"', 'workdir': '/tmp',});\ntext(r.output);",
			wantOK: true,
			checker: func(t *testing.T, got codexExecScript) {
				t.Helper()
				if got.Args["cmd"] != `echo "hi"` || got.Args["workdir"] != "/tmp" {
					t.Fatalf("Args=%#v", got.Args)
				}
			},
		},
		{
			name:   "first line pragma is host metadata",
			input:  "// @exec: {\"yield_time_ms\": 30000, \"max_output_tokens\": 1000}\nconst r = await tools.exec_command({cmd:\"pwd\"});",
			wantOK: true,
			checker: func(t *testing.T, got codexExecScript) {
				t.Helper()
				if got.Args["cmd"] != "pwd" {
					t.Fatalf("Args=%#v", got.Args)
				}
			},
		},
		{
			name:   "trailing text call is not a nested tool",
			input:  "const r = await tools.exec_command({cmd:\"pwd\"});\ntext(r.output);\ntext('done');",
			wantOK: true,
		},
		{
			name:   "comment mentioning tools is not a call",
			input:  "// tools.exec_command({cmd:\"rm -rf /\"}) would be wrong\nconst r = await tools.exec_command({cmd:\"pwd\"});",
			wantOK: true,
		},
		{
			name:   "block comment mentioning a loop is not control flow",
			input:  "/* for (;;) {} */ const r = await tools.exec_command({cmd:\"pwd\"});",
			wantOK: true,
		},
		{
			name:   "shell text containing for is not a loop",
			input:  "const r = await tools.exec_command({cmd:\"for f in *; do echo $f; done\"});",
			wantOK: true,
		},
		// JavaScript allows whitespace and comments around the access operator
		// and before the argument list. Each of these names the same call as the
		// canonical spelling, so the walk has to record the site instead of
		// stepping over it.
		{
			name:   "space before the access operator",
			input:  "const r = await tools .exec_command({cmd:\"pwd\"});",
			wantOK: true,
			checker: func(t *testing.T, got codexExecScript) {
				t.Helper()
				if got.Nested != codexNestedExecCommand || got.Args["cmd"] != "pwd" {
					t.Fatalf("got=%+v", got)
				}
			},
		},
		{
			name:   "space after the access operator",
			input:  "const r = await tools. exec_command({cmd:\"pwd\"});",
			wantOK: true,
		},
		{
			name:   "line break before the access operator",
			input:  "const r = await tools\n.exec_command({cmd:\"pwd\"});",
			wantOK: true,
			checker: func(t *testing.T, got codexExecScript) {
				t.Helper()
				if got.Args["cmd"] != "pwd" {
					t.Fatalf("Args=%#v", got.Args)
				}
			},
		},
		{
			name:   "block comment before the access operator",
			input:  "const r = await tools /* keep the cwd */ .exec_command({cmd:\"pwd\"});",
			wantOK: true,
		},
		{
			name:   "block comment before the argument list",
			input:  "const r = await tools.exec_command /* cwd */ ({cmd:\"pwd\"});",
			wantOK: true,
		},

		{name: "empty", input: "", wantOK: false},
		{name: "whitespace only", input: "   \n\t", wantOK: false},
		{name: "pragma only", input: "// @exec: {\"yield_time_ms\": 1000}\n", wantOK: false},
		{name: "no nested call", input: "text('hello');", wantOK: false},
		{name: "two call sites", input: "const a = await tools.exec_command({cmd:\"pwd\"});\nconst b = await tools.exec_command({cmd:\"ls\"});", wantOK: false},
		{name: "dynamic tools index", input: "await tools[\"exec_command\"]({cmd:\"pwd\"});", wantOK: false},
		{name: "dynamic tools index with a space", input: "await tools [\"exec_command\"]({cmd:\"pwd\"});", wantOK: false},
		{name: "bare tools identifier", input: "const t = tools;\ntext(t.name);", wantOK: false},
		// A call site the walk steps over would make a script with several calls
		// look like one, so the whitespace spellings must not hide a second site.
		{name: "dynamic index after a recorded call", input: "await tools.exec_command({cmd:\"a\"}); await tools [name]({cmd:\"b\"});", wantOK: false},
		{name: "line break spelling of a second call", input: "await tools\n.exec_command({cmd:\"a\"}); await tools.exec_command({cmd:\"b\"});", wantOK: false},
		{name: "property access only", input: "const f = tools.exec_command;\ntext('x');", wantOK: false},
		{name: "similar identifier is not the tools object", input: "await notools.exec_command({cmd:\"pwd\"});", wantOK: false},
		{name: "loop", input: "for (const c of ['a','b']) { await tools.exec_command({cmd:c}); }", wantOK: false},
		{name: "loop without space", input: "for(const c of ['a','b']){await tools.exec_command({cmd:c});}", wantOK: false},
		{name: "map over call sites", input: "await Promise.all(['a','b'].map((c) => tools.exec_command({cmd:c})));", wantOK: false},
		{name: "store state", input: "const r = await tools.exec_command({cmd:\"pwd\"});\nstore('dir', r.output);", wantOK: false},
		{name: "load state", input: "const d = load('dir');\nawait tools.exec_command({cmd:d});", wantOK: false},
		{name: "yield_control", input: "const r = await tools.exec_command({cmd:\"pwd\"});\nyield_control();", wantOK: false},
		{name: "ALL_TOOLS discovery", input: "const t = ALL_TOOLS.find((x) => x.name === 'exec_command');", wantOK: false},
		{name: "identifier argument", input: "await tools.exec_command(cmd);", wantOK: false},
		{name: "template substitution", input: "await tools.exec_command({cmd:`pwd ${dir}`});", wantOK: false},
		{name: "nested call inside the argument", input: "await tools.exec_command({cmd: await tools.exec_command({cmd:\"pwd\"})});", wantOK: false},
		{name: "non literal argument", input: "await tools.exec_command(1 + 2);", wantOK: false},
		{name: "unterminated string", input: "await tools.exec_command({cmd:\"pwd});", wantOK: false},
		{name: "unterminated call", input: "await tools.exec_command({cmd:\"pwd\"}", wantOK: false},
		{name: "binding is not a literal", input: "const patch = load('patch');\ntext(await tools.apply_patch(patch));", wantOK: false},
		// A call site that exists in the source is not evidence the call ran:
		// control flow can skip it, defer it, or run it twice. The parser has
		// to fail these toward keeping the source call instead of mapping them.
		{name: "false condition", input: "if (false) await tools.exec_command({cmd:\"pwd\"});", wantOK: false},
		{name: "short circuit", input: "false && await tools.exec_command({cmd:\"pwd\"});", wantOK: false},
		// A line break does not end the conditional expression, so the guard
		// lookback must cross it instead of reading only spaces and tabs.
		{name: "short circuit across a line break", input: "const r = flag &&\n  await tools.exec_command({cmd:\"git log\"});", wantOK: false},
		{name: "or across a line break", input: "const r = flag ||\n  await tools.exec_command({cmd:\"git log\"});", wantOK: false},
		{name: "ternary across a line break", input: "const r = flag ?\n  tools.exec_command({cmd:\"git log\"}) : null;", wantOK: false},
		{name: "comma across a line break", input: "foo(),\n  await tools.exec_command({cmd:\"git log\"});", wantOK: false},
		// A site nested in braces or parentheses belongs to an enclosing
		// construct whose execution the scan cannot prove, so it fails closed
		// even when no conditional operator precedes it.
		{name: "method body", input: "const o = { status() { return tools.exec_command({cmd:\"pwd\"}) } };\ntext(o.status);", wantOK: false},
		{name: "class method body", input: "class Repo { async status() { return await tools.exec_command({cmd:\"pwd\"}) } }\ntext(Repo);", wantOK: false},
		{name: "class static block", input: "class Repo { static { tools.exec_command({cmd:\"pwd\"}).then(() => {}); } }", wantOK: false},
		{name: "getter body", input: "const o = { get status() { return tools.exec_command({cmd:\"pwd\"}) } };", wantOK: false},
		{name: "parenthesized call", input: "const r = (await tools.exec_command({cmd:\"pwd\"}));", wantOK: false},
		{name: "top level block", input: "{ await tools.exec_command({cmd:\"pwd\"}); }", wantOK: false},
		// The `text(...)` argument list is the one transparent wrapper: a call
		// evaluates its arguments once, and Codex's apply_patch template wraps
		// the call this way. A conditional around the wrapper still guards the
		// nested call, and a non-helper callee stays unattributable.
		{name: "text wrapper", input: "text(await tools.exec_command({cmd:\"pwd\"}));", wantOK: true},
		{name: "text wrapper bound to a variable", input: "const r = text(await tools.exec_command({cmd:\"pwd\"}));", wantOK: true, checker: func(t *testing.T, got codexExecScript) {
			t.Helper()
			if got.Args["cmd"] != "pwd" {
				t.Fatalf("Args=%#v", got.Args)
			}
		}},
		{name: "conditional text wrapper across a line break", input: "const ok = flag &&\n  text(await tools.exec_command({cmd:\"pwd\"}));", wantOK: false},
		{name: "other call wrapper", input: "log(await tools.exec_command({cmd:\"pwd\"}));", wantOK: false},
		{name: "dotted text wrapper", input: "console.text(await tools.exec_command({cmd:\"pwd\"}));", wantOK: false},
		{name: "uncalled function body", input: "async function work() { await tools.exec_command({cmd:\"pwd\"}); }", wantOK: false},
		{name: "function called twice", input: "async function work() { await tools.exec_command({cmd:\"pwd\"}); }\nawait work(); await work();", wantOK: false},
		{name: "ternary", input: "await (ready ? tools.exec_command({cmd:\"pwd\"}) : null);", wantOK: false},
		// Only an immutable, still-in-scope declaration can name the value a
		// freeform call received; anything reassignable or out of order cannot.
		{name: "let binding reassigned", input: "let patch = \"a\"; patch = \"b\";\ntext(await tools.apply_patch(patch));", wantOK: false},
		{name: "const binding compound reassigned", input: "const patch = \"a\"; patch += \"b\";\ntext(await tools.apply_patch(patch));", wantOK: false},
		{name: "binding declared after the call", input: "await tools.apply_patch(patch); const patch = \"x\";", wantOK: false},
		{name: "binding out of scope at the call", input: "{ const patch = \"x\"; }\ntext(await tools.apply_patch(patch));", wantOK: false},
		// A const initializer that is an expression, not a single literal, can
		// only be resolved by running the script, so the binding must fail
		// closed and keep the source call rather than binding a wrong value.
		{name: "binding concatenated literal", input: "const patch = \"a\" + \"b\";\ntext(await tools.apply_patch(patch));", wantOK: false},
		{name: "binding adjacent strings", input: "const patch = \"a\" \"b\";\ntext(await tools.apply_patch(patch));", wantOK: false},
		{name: "binding ternary", input: "const patch = ready ? \"a\" : \"b\";\ntext(await tools.apply_patch(patch));", wantOK: false},
		// A single literal followed by a trailing comment still binds.
		{name: "binding single literal with trailing comment", input: "const patch = \"*** Begin Patch\\n*** End Patch\" /* x */;\ntext(await tools.apply_patch(patch));", wantOK: true, checker: func(t *testing.T, got codexExecScript) {
			t.Helper()
			if got.Input != "*** Begin Patch\n*** End Patch" {
				t.Fatalf("Input=%q", got.Input)
			}
		}},
		// An invalid pragma is one Codex refuses to run, so the script never
		// executed its nested call and must not be mapped to one.
		{name: "pragma not json", input: "// @exec: not-json\nawait tools.exec_command({cmd:\"pwd\"});", wantOK: false},
		{name: "pragma unknown field", input: "// @exec: {\"yield_time_ms\":1,\"unexpected\":1}\nawait tools.exec_command({cmd:\"pwd\"});", wantOK: false},
		{name: "pragma fractional value", input: "// @exec: {\"yield_time_ms\":1.5}\nawait tools.exec_command({cmd:\"pwd\"});", wantOK: false},
		{name: "pragma exponent value", input: "// @exec: {\"max_output_tokens\":1e3}\nawait tools.exec_command({cmd:\"pwd\"});", wantOK: false},
		{name: "pragma negative value", input: "// @exec: {\"yield_time_ms\":-1}\nawait tools.exec_command({cmd:\"pwd\"});", wantOK: false},
		{name: "pragma empty object", input: "// @exec: {}\nawait tools.exec_command({cmd:\"pwd\"});", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCodexExecScript(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v (script=%q, got=%+v)", ok, tt.wantOK, tt.input, got)
			}
			if !ok {
				return
			}
			if tt.checker != nil {
				tt.checker(t, got)
			}
		})
	}
}

// A regex literal is data, not code: a nested call spelled inside one must
// not be mapped — the contract is that a mapping proves a call really ran —
// and a keyword-preceded regex must not be misread as division.
func TestParseCodexExecScript_RegexLiteralIsNotCode(t *testing.T) {
	for _, script := range []string{
		"const re = /tools.exec_command({cmd:\"ls\"})/;\ntext(\"done\");\n",
		"return /tools.exec_command({cmd:\"ls\"})/;\n",
		"const re = /[//]tools.apply_patch(/;\ntext(\"done\");\n",
	} {
		if _, ok := parseCodexExecScript(script); ok {
			t.Fatalf("regex body mapped as a nested call: %q", script)
		}
	}
}

// An unterminated regex literal cannot be walked reliably; the scan fails
// closed and the source call is kept.
func TestParseCodexExecScript_UnterminatedRegexFailsClosed(t *testing.T) {
	if _, ok := parseCodexExecScript("const re = /tools.exec_command(\"ls\");\n"); ok {
		t.Fatal("unterminated regex mapped; want the source exec call kept")
	}
}

// Division must keep walking as code: a slash after a value is not a regex
// start.
func TestParseCodexExecScript_DivisionStaysCode(t *testing.T) {
	script, ok := parseCodexExecScript("const half = 10 / 2;\ntools.exec_command({cmd:\"ls\"});\n")
	if !ok || script.Nested != codexNestedExecCommand {
		t.Fatalf("division misparsed as regex: ok=%v script=%+v", ok, script)
	}
}

func TestParseCodexJSValue(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		want   any
		wantOK bool
	}{
		{name: "bare key object", src: `{cmd:"pwd",workdir:"/tmp"}`, want: map[string]any{"cmd": "pwd", "workdir": "/tmp"}, wantOK: true},
		{name: "quoted keys", src: `{"cmd":"pwd","n":3}`, want: map[string]any{"cmd": "pwd", "n": float64(3)}, wantOK: true},
		{name: "nested", src: `{a:{b:[1,2,true,null]}}`, want: map[string]any{"a": map[string]any{"b": []any{float64(1), float64(2), true, nil}}}, wantOK: true},
		{name: "empty object", src: `{}`, want: map[string]any{}, wantOK: true},
		{name: "empty array", src: `[]`, want: []any{}, wantOK: true},
		{name: "negative and float", src: `-1.5e3`, want: float64(-1500), wantOK: true},
		{name: "double quoted string", src: `"a\"b"`, want: `a"b`, wantOK: true},
		{name: "single quoted string", src: `'a\'b'`, want: `a'b`, wantOK: true},
		{name: "template without substitution", src: "`pwd`", want: "pwd", wantOK: true},
		{name: "unicode escape", src: `"\u0041\u{42}"`, want: "AB", wantOK: true},
		{name: "surrogate pair", src: `"\ud83d\ude00"`, want: "\U0001F600", wantOK: true},
		{name: "hex escape", src: `"\x41"`, want: "A", wantOK: true},
		{name: "line continuation", src: "\"a\\\nb\"", want: "ab", wantOK: true},
		{name: "escape sequences", src: `"\n\t\r\b\f\v\0A"`, want: "\n\t\r\b\f\v\x00A", wantOK: true},
		{name: "hex and unicode escapes", src: `"\x42\u00e9\u4e2d"`, want: "B\u00e9\u4e2d", wantOK: true},
		{name: "escaped quote and backslash", src: "\"\\\\ \\\" \\' /\"", want: "\\ \" ' /", wantOK: true},
		{name: "line continuation", src: "\"a\\\n b\"", want: "a b", wantOK: true},
		{name: "comments inside an argument source", src: "{ /* c */ cmd : \"pwd\" ,// tail\n}", want: map[string]any{"cmd": "pwd"}, wantOK: true},
		{name: "trailing whitespace", src: "  {cmd:\"pwd\"}  ", want: map[string]any{"cmd": "pwd"}, wantOK: true},
		{name: "trailing comma", src: `{cmd:"pwd",}`, want: map[string]any{"cmd": "pwd"}, wantOK: true},

		{name: "empty", src: "", wantOK: false},
		{name: "identifier", src: "cmd", wantOK: false},
		{name: "call", src: "f()", wantOK: false},
		{name: "template substitution", src: "`a${b}`", wantOK: false},
		{name: "trailing expression", src: "{cmd:\"pwd\"} + 1", wantOK: false},
		{name: "unquoted value", src: `{cmd:pwd}`, wantOK: false},
		{name: "missing colon", src: `{cmd "pwd"}`, wantOK: false},
		{name: "unterminated object", src: `{cmd:"pwd"`, wantOK: false},
		{name: "unterminated string", src: `"pwd`, wantOK: false},
		{name: "array of identifiers", src: `[a]`, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCodexJSValue(tt.src)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v (src=%q, got=%#v)", ok, tt.wantOK, tt.src, got)
			}
			if !ok {
				return
			}
			if !jsValueEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func jsValueEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !jsValueEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsValueEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

func TestParseCodexExecResult(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantStatus string
		wantOutput string
		wantOK     bool
	}{
		{
			name:       "completed",
			output:     "Script completed\nWall time 1.2 seconds\nOutput:\nhello\n",
			wantStatus: codexExecStatusCompleted,
			wantOutput: "hello\n",
			wantOK:     true,
		},
		{
			name:       "failed with script error body",
			output:     "Script failed\nWall time 0.0 seconds\nOutput:\nScript error:\nTypeError: x is not a function",
			wantStatus: codexExecStatusFailed,
			wantOutput: "Script error:\nTypeError: x is not a function",
			wantOK:     true,
		},
		{
			name:       "terminated",
			output:     "Script terminated\nWall time 3.0 seconds\nOutput:\n",
			wantStatus: codexExecStatusTerminated,
			wantOutput: "",
			wantOK:     true,
		},
		{
			name:       "yielded keeps the cell id in the status",
			output:     "Script running with cell ID 7\nWall time 10.0 seconds\nOutput:\npartial",
			wantStatus: "Script running with cell ID 7",
			wantOutput: "partial",
			wantOK:     true,
		},
		{
			name:       "overhead variant header",
			output:     "Script completed\nWall time 1.234 seconds (code-mode 1.200 seconds; overhead 0.034 seconds)\nOutput:\nbody",
			wantStatus: codexExecStatusCompleted,
			wantOutput: "body",
			wantOK:     true,
		},
		{
			name:       "bare header without body",
			output:     "Script completed\nWall time 0.1 seconds\nOutput:",
			wantStatus: codexExecStatusCompleted,
			wantOutput: "",
			wantOK:     true,
		},
		{name: "plain tool output", output: "some output", wantOK: false},
		{name: "unknown status", output: "Script exploded\nWall time 1.0 seconds\nOutput:\nx", wantOK: false},
		{name: "missing wall time", output: "Script completed\nOutput:\nx", wantOK: false},
		{name: "empty", output: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCodexExecResult(tt.output)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v (got=%+v)", ok, tt.wantOK, got)
			}
			if !ok {
				return
			}
			if got.Status != tt.wantStatus || got.Output != tt.wantOutput {
				t.Fatalf("got %+v, want status=%q output=%q", got, tt.wantStatus, tt.wantOutput)
			}
			if want := tt.wantStatus == codexExecStatusCompleted; got.succeeded() != want {
				t.Fatalf("succeeded()=%v want %v", got.succeeded(), want)
			}
		})
	}
}
