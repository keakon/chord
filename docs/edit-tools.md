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
| **Typical models** | gpt-5.5, gpt-5.3-codex, o3, o4 | Claude, Qwen, GLM, MiniMax, DeepSeek, Gemini |

## Tool Selection

Chord **automatically selects** the appropriate tool based on the active model:

- **GPT/o-series models** → `apply_patch` (Codex envelope)
- **All other models** → `edit` (old_string/new_string)

You don't need to choose manually—the system exposes only the appropriate tool to each model.

When a patch-native (GPT/o-series) model keeps `apply_patch`, Chord also hides `write` and `delete`: the envelope subsumes them (`*** Add File:` creates, `*** Delete File:` removes), matching the native Codex CLI surface those models are trained on. Fallback pairings keep `write`/`delete`: a non-GPT model that only got `apply_patch` because `edit` is disabled still sees them, and a patch-native model downgraded to `edit` needs `write` to create files at all.

---

## apply_patch Tool (Codex envelope)

### Format

The single `patch` argument carries a complete Codex envelope. It can contain any number of file operations:

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

Hunks apply in order; each hunk is matched at the first position after the previous hunk's application point. Repeated plain `*** Update File:` sections for the same normalized path follow Codex ordering semantics: each section patches the previous section's in-memory result, and the whole envelope is committed as one file mutation (so a later mismatch leaves the workspace unchanged rather than exposing Codex's partial-write behavior). Lines inside a hunk keep their raw `' '`/`+`/`-` prefix, so file content that itself begins with `***` followed by a space stays ordinary context—only lines beginning with an unprefixed `***` followed by a space are protocol markers.

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

Legacy single-file arguments (`{"path": ..., "patch": "@@\n..."}`) are still accepted and normalized into an Update envelope for that path.

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

All operations in one envelope are planned and validated from a single filesystem snapshot **before any file is modified**. If planning fails (missing file, overlapping operations on the same path, an `Add` target that already exists), no files change. If a file changes on disk between planning and commit, the whole patch is rejected. If a write fails mid-commit, already-committed operations are rolled back.

### Error Messages

- **"hunk not found (N/M)"**: The indicated hunk does not match the current file. The error includes a short expected-line preview and, when available, explains that the text occurs only within a longer line or earlier than the preceding hunk. Re-read the target range and rebuild the hunk from complete current lines.
- **"hunk has no context or removed lines; add unchanged context"**: A hunk contains only `+` lines; include at least one context or `-` line to anchor it.
- **"cannot add file that already exists"**: `*** Add File:` targets an existing path; use `*** Update File:` instead.
- **"apply_patch contains overlapping operations"**: Two operations in one envelope touch paths where one contains the other (for example `dir` and `dir/file`), or resolve to the same file through different names; merge them into one operation. Repeated `*** Update File:` sections for the exact same path are allowed and apply in order.
- **"changed after planning"**: The file was modified between validation and commit; nothing was written—retry against the current content.

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

- **`old_string`** (required): Exact text to find. Must match indentation, whitespace, and newlines exactly.
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

- **"old_string not found in file"**: The exact text doesn't exist. Check whitespace, indentation, and newlines.
- **"old_string found N times"**: Multiple matches found. Either:
  - Add more context to make it unique
  - Set `replace_all: true` if you want to replace all occurrences
- **"old_string and new_string are identical"**: No change needed.

### Trailing Newline Tolerance

The tool automatically handles minor trailing newline differences:

- If `old_string` has a final `\n` but the match doesn't (or vice versa), and the match is unique, the edit proceeds.
- This reduces retries caused by newline mismatches.

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
  apply_patch: deny  # GPT/o-series models fall back to edit
```

**Permission Fallback Rules**:

- If only `edit` is configured, `apply_patch` inherits the same permission
- If only `apply_patch` is configured, `edit` inherits the same permission
- This includes `deny`: `edit: deny` disables `apply_patch` too unless `apply_patch` also has its own explicit rule
- If both are configured, each tool uses its own explicit rule
- A single `edit` or `apply_patch` rule applies to both tools and overrides wildcard rules

**Examples**:

- `edit: allow` → both tools allowed; GPT/o-series models normally see `apply_patch`, other models normally see `edit`
- `edit: allow, apply_patch: deny` → apply_patch denied, edit allowed; GPT/o-series models fall back to `edit`
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

`apply_patch` matches hunk context in passes: exact match first, then ignoring trailing whitespace, then ignoring surrounding whitespace, then normalizing common Unicode quote, dash, and whitespace variants. Repeated blocks still need enough nearby context (or an `*** End of File` marker) to make the intended location clear.

For any file that can be decoded as text, a final fallback also treats common Chinese and ASCII punctuation as equivalent. This includes source files, dotenv files such as `.env.example`, and extensionless text files. The fallback applies only when the complete hunk has one unique match. It preserves punctuation from the current file in unchanged parts of replacement lines and reports its use in the tool result. Ambiguous matches are rejected, and a fragment occurring inside a longer line is diagnostic only—not an automatic substring edit. Binary or otherwise undecodable files do not enter this fallback because text decoding fails before hunk matching.

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
A: By default, unrecognized models use the `edit` (replace) tool. GPT/o-series models use `apply_patch`.

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
