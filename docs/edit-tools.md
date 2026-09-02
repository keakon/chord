# Edit Tools: apply_patch vs Edit

Chord provides two complementary tools for editing files, optimized for different model training backgrounds.

## Quick Comparison

| Feature | **apply_patch Tool** | **Edit (Replace) Tool** |
|---------|----------------------|-------------------------|
| **Format** | Codex patch envelope (`*** Begin Patch` … `*** End Patch`) with `@@` hunks | Text matching (old_string → new_string) |
| **Best for** | Models trained with OpenAI's `apply_patch` | Models trained with Claude Code or similar replace interfaces |
| **Scope** | Multiple files per call: add, update, delete, move | One existing file per call |
| **Position control** | Context lines + optional header anchors | Exact string matching |
| **Multi-occurrence** | N/A (context-driven) | `replace_all` parameter |
| **Typical models** | gpt-5.5, gpt-5.3-codex, codex-auto-review | Claude, Qwen, GLM, MiniMax, DeepSeek, Gemini |

## Tool Selection

Chord **automatically selects** the appropriate tool based on the active model:

- **gpt-5 and later gpt major families (gpt-5, gpt-5-mini, gpt-5-nano, gpt-5-codex, any `gpt-5.*` name, and future majors like gpt-6) and `codex-auto-review`** → `apply_patch` (Codex envelope)
- **All other models** (gpt-3.5, gpt-4/4o, gpt-oss-*, o-series, Claude, Qwen, GLM, DeepSeek, Gemini, …) → `edit` (old_string/new_string)

The gpt-4/4o, gpt-3.5, and o-series families are **not** patch-native: the `apply_patch` tool did not exist when they were trained (it was introduced with GPT-5 in August 2025), and measured results are negative or untrained. Every gpt family from gpt-5 onward defaults to the patch tool surface, matching the Codex model catalog; this includes future majors (gpt-6, ...) since `apply_patch` is first-party Codex training data that stays in OpenAI training across generations. If a future family ever drops the patch signal, `compat.apply_patch.enabled: false` opts it out.

When a patch-native model keeps `apply_patch`, Chord also hides `write` and `delete`: the envelope subsumes them (`*** Add File:` creates, `*** Delete File:` removes), matching the native Codex CLI surface those models are trained on. Fallback pairings keep `write`/`delete`: a non-patch-native model that only got `apply_patch` because `edit` is disabled still sees them, and a patch-native model downgraded to `edit` needs `write` to create files at all.

### Freeform (custom tool) emission

On OpenAI-compatible **Responses** endpoints, a gpt-5-and-later family model or `codex-auto-review` additionally receives `apply_patch` as a **freeform custom tool** (`type: "custom"` with a Lark grammar), instead of a JSON function tool. Freeform gives the model grammar-constrained decoding — it cannot produce a syntax-invalid patch — and avoids JSON escaping overhead. All other models receive the JSON function shape, and non-Responses endpoints always use the function shape (they have no custom tool type).

Hosts that accept Responses requests but reject custom tools do not get a built-in exception: a patch-native model there will emit the freeform shape by default, and the gateway rejects it with an actionable error. Set `compat.apply_patch.freeform: false` for such hosts to force the JSON function shape.

### Overriding the defaults

Every default above can be overridden per provider or per model under `compat.apply_patch` (three-state: omit to keep the inference):

```yaml
providers:
  my-relay:
    type: responses
    compat:
      apply_patch:
        enabled: true   # tool surface: keep apply_patch (hide edit + write/delete)
        freeform: false # wire shape: JSON function tool, not custom
  openai:
    type: responses
    models:
      gpt-5.5:
        compat:
          apply_patch:
            freeform: false # model-level override: name looks freeform but the gateway is not
```

- `enabled: true` adopts the full patch-native semantics for any model (patch kept, `edit`/`write`/`delete` hidden, patch-only prompt guidance).
- `enabled: false` forces the edit surface even for patch-native models.
- `freeform: true` forces the custom tool shape; `freeform: false` forces the JSON function shape.

If a gateway lowers a custom tool into `{"input": "..."}` instead of `{"patch": "..."}`, Chord reports an actionable error pointing at `compat.apply_patch.freeform: false`; set it and the request will be sent as a function tool.

---

## apply_patch Tool (Codex envelope)

### Format

The single `patch` argument carries the Codex patch body. Chord accepts the normal complete envelope and also repairs a missing `*** Begin Patch` and/or `*** End Patch` wrapper before parsing. Inside the body, you can include any number of file operations:

```text
*** Begin Patch
*** Update File: src/main.go
@@ func main() {
 context line
-removed line
+added line
 context line
*** Add File: docs/new.md
+# New document
+First line.
*** Delete File: tmp/old.txt
*** End Patch
```

Supported operations:

- **`*** Add File: path`** — create a file; every body line starts with `+`.
- **`*** Update File: path`** — modify a file with one or more `@@` hunks.
- **`*** Move to: newpath`** — rename while updating; must appear directly after its `*** Update File:` line. A pure rename still needs one (possibly context-only) hunk.
- **`*** Delete File: path`** — remove a file; no body.
- **`*** End of File`** — after a hunk, pins that hunk to the file tail (useful when the same block also appears earlier).

Hunks apply in order; each hunk is matched at the first position after the previous hunk's application point. Repeated plain `*** Update File:` sections for the same normalized path follow Codex ordering semantics: each section patches the previous section's in-memory result, and that file is committed as one mutation (so a later mismatch leaves that file unchanged rather than exposing Codex's partial-write behavior). Lines inside a hunk keep their raw `' '`/`+`/`-` prefix, so file content that itself begins with `***` followed by a space stays ordinary context—only lines beginning with an unprefixed `***` followed by a space are protocol markers.

### When to Use

- Your model has been trained with OpenAI's `apply_patch` or similar patch-based interfaces
- The change spans several files, or creates/deletes/moves files alongside content edits
- You need precise positional control through context lines

### Example

```json
{
  "patch": "*** Begin Patch\n*** Update File: main.go\n@@\n func main() {\n-\tfmt.Println(\"hello\")\n+\tfmt.Println(\"hello, world\")\n }\n*** End Patch"
}
```

### Optional Header Anchors

You can add text after `@@` to help locate ambiguous blocks:

```diff
@@ func processUser(id int) error {
 if id < 0 {
-  return errors.New("invalid")
+  return fmt.Errorf("invalid user ID: %d", id)
 }
```

**Important**: Only use headers you've verified exist in the file. Headers are soft anchors: when the header text is not found, matching falls back to the hunk body alone.

### Transactional Behavior

All operations in one envelope are planned from a single filesystem snapshot **before any file is modified**. Envelope-wide preflight failures—such as malformed syntax, unsafe overlapping paths, or an unreadable snapshot—leave every file unchanged. An operation-level failure, such as a missing update source or an existing `Add` target, rejects that file group while independent file groups can still commit. If any planned file changes on disk before commit, the commit is rejected without writing its successful subset. If a write fails mid-commit, already-written mutations from that commit attempt are rolled back.

Atomicity is per file, not per envelope. Each file is an independent unit: all operations that touch one file (including repeated `*** Update File:` sections for that same path) commit together, or are rolled back together. When one file fails the other independent files in the same envelope are still applied and written to disk. The failure result lists the committed changes (which do not need to be redone), explains which operation groups were not applied and why, and tells you to rebuild the failed operations from current file contents and resubmit only those. Resolve each reported failure and submit the rebuilt operations against the current workspace; do not re-emit committed files, and the result does not repeat the submitted patch.

A failed file drags its whole group: if an earlier operation on the same file matched in memory but a later one failed, all of that file's operations are reported as unapplied and omitted from the final plan. Earlier successful prerequisite groups remain eligible to commit, while groups that depend on the discarded group are omitted with it. The result lists every operation carried along (including the ones that matched), so the model can rebuild the complete failed dependency chain from its own submitted patch.

A move binds both its source and destination into the same dependency boundary. If the move fails, later operations touching either path are also rejected and included in the unapplied operations. The same rule applies when a source group fails after an earlier move appeared to succeed: operations that depended on the moved destination are rolled back with it. This keeps the failure complete instead of reporting a dependent destination edit as committed after its prerequisite was discarded, so a rebuilt operation does not miss this dependency chain.

### Error Messages

- **"hunk not found (N/M)"**: The indicated hunk does not match the current file. The error identifies the first expected complete line, or labels it as a prefix when the diagnostic preview is truncated. When available, it also explains that the text occurs only within a longer line or earlier than the preceding hunk. If earlier hunks of the same file matched in memory but a later one failed, none of that file group's hunks were applied. Re-read the target range, rebuild the failing hunk from current complete lines, and keep the group's other hunks with it when you resubmit.
- **"cannot add file that already exists"**: `*** Add File:` targets an existing path; use `*** Update File:` instead.
- **"apply_patch contains overlapping operations"**: Two operations in one envelope touch paths where one contains the other (for example `dir` and `dir/file`), or resolve to the same file through different names; merge them into one operation. Repeated `*** Update File:` sections for the exact same path are allowed and apply in order.
- **"changed after planning"**: The file was modified between validation and commit; nothing was written—retry against the current content.
- **"apply_patch partially applied: N changes committed, M file groups not applied: ..."**: One or more independent changes committed while other operation groups were omitted (the singular form uses "change" / "file group"). The changes under "Applied patch" are already on disk — do not redo them, and the failure does not echo the submitted patch back. "Not applied" lists each omitted operation group's path and cause; resolve each cause, rebuild those operations from current file contents, and submit only those.

---

## Edit (Replace) Tool

### Format

```json
{
  "path": "main.go",
  "old_string": "fmt.Println(\"hello\")",
  "new_string": "fmt.Println(\"hello, world\")",
  "replace_all": false
}
```

### When to Use

- Your model hasn't been specifically trained on patch formats
- The change is straightforward: find exact text → replace with new text
- You want to rename a variable/identifier across a file (`replace_all: true`)

### Parameters

- **`old_string`** (required): Exact text to find. Must match indentation, whitespace, and newlines exactly. As a last-resort fallback, punctuation variants are tolerated (see [Punctuation Tolerance](#punctuation-tolerance) below).
- **`new_string`** (required): Replacement text.
- **`replace_all`** (optional): `true` to replace all occurrences, `false` (default) to replace only the first.

### Example: Single Replacement

```json
{
  "path": "server.go",
  "old_string": "const port = 8080",
  "new_string": "const port = 3000"
}
```

### Example: Rename Variable

```json
{
  "path": "handler.go",
  "old_string": "userID",
  "new_string": "userId",
  "replace_all": true
}
```

### Error Messages

- **"old_string not found in file"**: The exact text doesn't exist even under punctuation tolerance. Check whitespace, indentation, and newlines. When the mismatch is a character-level difference (a dropped or extra rune), the error also points at the closest matching block in the file — its line number, how similar it is, and the exact differing lines — so you can see the one-character mistake (for example a missing `)` or a doubled `,,`) without re-reading the whole file. When whole lines have drifted so far that the shown lines cannot rebuild the target, the error instead names the drift and hands over `read` coordinates (offset/limit) for the closest-match range, or suggests a smaller 2-4 line anchor.
- **"old_string found N times"**: Multiple matches found. Either:
  - Add more context to make it unique
  - Set `replace_all: true` if you want to replace all occurrences
- **"old_string and new_string are identical"**: No change needed.

When the same target file repeatedly fails approximate matching on `edit`/`apply_patch`, the agent appends a note to the model-visible result (from the second failure on) telling it to read the target range fresh — or switch to `write` for a whole-block replacement — instead of retyping the same old text from memory. The note does not appear in the UI, and any successful result or a new turn resets the count.

### Invisible Character Cleaning

The write paths of `edit`, `apply_patch`, and `write` strip zero-width formatting characters and floating combining marks that models leak into tool arguments (zero-width space, zero-width non-joiner, zero-width joiner outside emoji sequences, word joiner, mid-stream BOM, soft hyphen; and a diacritic with no visible base — at the start of the text or preceded only by whitespace — such as a stray macron after a space). These runes carry no content — a floating mark cannot change what any character means — so stripping them cannot change what the text means; leaving them in would plant invisible bytes in the file. When any are removed, the tool result reports exactly which code points were cleaned (for example `U+200B×2, U+0304×1`), so the model learns to stop emitting them. A combining mark over any visible base is kept — letter, digit, symbol, or punctuation — so legitimate diacritics (Vietnamese/Arabic/Devanagari text, stacked marks) and sequences such as a U+0305 overline over a digit in math notation survive untouched. In `apply_patch` the clean is limited to the lines the patch adds: existing content elsewhere in the file is never scanned or rewritten by this clean.

### Trailing Newline Tolerance

The tool automatically handles minor trailing newline differences:

- If `old_string` has a final `\n` but the match doesn't (or vice versa), and the match is unique, the edit proceeds.
- This reduces retries caused by newline mismatches.

### Punctuation Tolerance

When exact matching and trailing-newline matching both fail, the tool retries with common punctuation variants treated as equivalent — the same 1:1 normalization surface `apply_patch` uses:

- Curly vs straight quotes (`“ ”` ↔ `" "`, `‘ ’` ↔ `' '`)
- Dashes (`–`, `—`, `−` ↔ `-`)
- Full-width vs half-width CJK punctuation (`,` `;` `:` `.` `!` `?` `(` `)`)

The fallback applies only when the normalized `old_string` has one unique match, reports its use in the tool result, and preserves the file's original punctuation for unchanged context. Multiple normalized matches error with the "found N times" message.

A single space directly adjacent to a separator punctuation mark is also treated as optional — `：` and `:` with a trailing space (and `:the` when the space is dropped) match the same text, as does an inter-word space (`diff and` and `diffand`). This covers models that tokenize `": "` as one token and re-emit it as `：`, or drop/insert a word-boundary space. The folding is deliberately narrow: only one space right after `,` `;` `:` `.` `!` `?` `(` (or right before `)`) or between two word characters is optional. Double spaces, spaces after quotes or dashes, indentation, and newlines stay significant, so a genuine layout mismatch still fails with "old_string not found" instead of silently applying a wrong edit. The result text reports when the tolerance was used; the tool description deliberately does not advertise it, so models still aim for exact matches.

A combining mark with no visible base (at the start of a line or preceded only by whitespace) is folded out during this normalization: it is a tokenizer artifact that never exists in real file content at that position, but does leak into copied text when a tokenizer splits a heading like `### [U+0304].2.1`. A mark over any visible base — letter, digit, symbol, or punctuation — is kept untouched, so legitimate diacritics (Arabic, Devanagari, Vietnamese, including stacked sequences) and marks on digits or symbols (math overlines) are never folded away.

---

## Recommended Workflow

Neither edit tool requires a prior `read`: both tools read current on-disk content at execution time. For reliable edits, still follow these recommendations:

1. **Inspect the target area first** when you have not already verified the exact text, path, or hunk anchor. `read`, `grep`, or `lsp` are good ways to do that.
2. **Use the smallest unique block** (2-4 lines). Large context blocks are more likely to become stale.
3. **Re-read after failures**. If a hunk or string match fails, the file may have changed—read it again before retrying.

---

## Task-Specific Guidance

### For Localized Changes

Both tools work well. Choose based on model training:

- **apply_patch**: Better when you need positional control (e.g., "change the first occurrence in this function").
- **Edit**: Better for simple find-replace with clear boundaries.

### For Renaming/Refactoring

- **Edit with `replace_all: true`**: Rename a variable across one file.
- **LSP tool**: For symbol-aware renames across multiple files.

### For Large-Scale Changes

- Creating, deleting, or moving files → `apply_patch` does this natively (`*** Add File:` / `*** Delete File:` / `*** Move to:`); models on `edit` use **Write** and **Delete**
- Batch text replacements across many files → Use **Shell** with `sd` or `sed`
- Symbol renames across files → Use **LSP**

---

## Permissions

Both tools share the **file permission family** (path-based authorization). A single approval for a path applies to both editing tools.

In permission rules, hook filters, and skill `allowed_tools`, the formal names are `edit` and `apply_patch`. `patch` is accepted as a legacy alias for `apply_patch`, so existing configurations keep working.

### Permission Configuration

Configure permissions for either edit tool name; a rule for one editor applies to the other editor unless the other editor also has an explicit rule:

**Unified Configuration** (recommended):

```yaml
permission:
  edit: allow  # Both apply_patch and edit tools allowed
```

**Disable One Format** (advanced):

```yaml
permission:
  edit: allow
  apply_patch: deny  # patch-native models (gpt-5 family/codex-auto-review) fall back to edit
```

**Permission Fallback Rules**:

- If only `edit` is configured, `apply_patch` inherits the same permission
- If only `apply_patch` is configured, `edit` inherits the same permission
- This includes `deny`: `edit: deny` disables `apply_patch` too unless `apply_patch` also has its own explicit rule
- If both are configured, each tool uses its own explicit rule
- A single `edit` or `apply_patch` rule applies to both tools and overrides wildcard rules

**Examples**:

- `edit: allow` → both tools allowed; patch-native models (gpt-5 family/codex-auto-review) normally see `apply_patch`, other models normally see `edit`
- `edit: allow, apply_patch: deny` → apply_patch denied, edit allowed; patch-native models fall back to `edit`
- `apply_patch: allow, edit: deny` → apply_patch allowed, edit denied; non-GPT models fall back to `apply_patch`
- `*: deny, apply_patch: allow` → both tools allowed (apply_patch rule is inherited by edit)
- `*: allow, apply_patch: deny` → both tools denied (edit inherits the apply_patch deny)

---

## Technical Notes

### Why Two Tools?

Models exhibit strong preferences based on their training data:

- GPT models have seen extensive `@@`-style patches in their training (OpenAI's `apply_patch`)
- Claude, Qwen, and similar models perform better with intuitive find-replace formats

Empirical testing (Aider's edit-bench, internal chord metrics) shows:

- **GPT models**: 91-96% success with the patch format, ~70% with replace
- **Non-GPT models**: 81-96% success with the replace format, 44-79% with patch

### Matching Tolerance

`apply_patch` matches hunk context in three exact passes: exact match first, then ignoring trailing whitespace, then ignoring surrounding whitespace. Punctuation/whitespace tolerance (quotes, dashes, full-width CJK punctuation, and the optional space after separator punctuation) is deliberately **not** a fourth pass: it is a single separate step that must land in exactly one place, and an ambiguous tolerant match is rejected with the candidate lines named instead of silently taking the first one. Repeated blocks still need enough nearby context (or an `*** End of File` marker) to make the intended location clear.

For any file that can be decoded as text, a final fallback also treats common Chinese and ASCII punctuation as equivalent, and — like the `edit` tool — treats a single space adjacent to a separator punctuation mark as optional (`：`, `:` followed by a space, and `:the` match the same line). Both tools share the same normalization and the same preservation rules. This includes source files, dotenv files such as `.env.example`, and extensionless text files. The fallback applies only when the complete hunk has one unique match. It preserves punctuation from the current file in unchanged parts of replacement lines and reports its use in the tool result. Ambiguous matches are rejected, and a fragment occurring inside a longer line is diagnostic only—not an automatic substring edit. Binary or otherwise undecodable files do not enter this fallback because text decoding fails before hunk matching.

### Token Efficiency

- **Replace**: Generally 20-40% fewer tokens for small edits (no context lines required)
- **apply_patch**: More tokens due to context and envelope, but better precision for complex and multi-file edits

### Implementation

- Both tools validate hunks/strings before writing
- Both support LSP integration (workspace notifications)
- Both participate in the same concurrent editing controls (path-based locking)
- Both generate unified diffs for display (regardless of input format)

---

## Migration from Single-Tool Systems

If you're upgrading from a system with only one edit tool:

1. **No action required**: Chord automatically selects the right tool per model
2. **SessionImport compatibility**: Historical edit calls are mapped:
   - `codex` provider → `apply_patch` tool
   - Other providers → `edit` tool
3. **Permission continuity**: Both tools share the file permission family, and `patch` rules are read as `apply_patch`

---

## FAQ

**Q: Can I force a specific tool?**
A: The tool selection is automatic and model-specific. Overriding it may reduce success rates.

**Q: What if my model isn't recognized?**
A: By default, unrecognized models use the `edit` (replace) tool. gpt-5-and-later family names (gpt-5, gpt-5-mini, gpt-5-nano, gpt-5-codex, any `gpt-5.*` name, future majors like gpt-6) and `codex-auto-review` use `apply_patch`; you can override any model via `compat.apply_patch.enabled`.

**Q: Do both tools support the same file types?**
A: Yes. Both work with any text file (detected encoding: UTF-8, UTF-16, GB18030, etc.). Binary files are rejected.

**Q: Can I use both tools in the same conversation?**
A: Only one tool is visible at a time, based on the active model. You won't see both simultaneously.

**Q: What about `hashline` (content-addressed anchors)?**
A: Not currently enabled. It's a potential future enhancement after validation in production workloads.

---

## See Also

- [Tool Reference](./tools.md) – All available tools
- [Permission System](./permissions-and-safety.md) – How file access control works
- [LSP Integration](./tools.md) – Symbol-aware operations
