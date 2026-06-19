# Edit Tools: Patch vs Edit

Chord provides two complementary tools for editing files, optimized for different model training backgrounds.

## Quick Comparison

| Feature | **Patch Tool** | **Edit (Replace) Tool** |
|---------|----------------|-------------------------|
| **Format** | Unified diff hunks (`@@`-style) | Text matching (old_string → new_string) |
| **Best for** | Models trained with OpenAI's `apply_patch` | Models trained with Claude Code or similar replace interfaces |
| **Position control** | Context lines + optional header anchors | Exact string matching |
| **Multi-occurrence** | N/A (context-driven) | `replace_all` parameter |
| **Typical models** | gpt-5.5, gpt-5.3-codex, o3, o4 | Claude, Qwen, GLM, MiniMax, DeepSeek, Gemini |

## Tool Selection

Chord **automatically selects** the appropriate tool based on the active model:

- **GPT/o-series models** → Patch tool (@@-style hunks)
- **All other models** → Edit tool (old_string/new_string)

You don't need to choose manually—the system exposes only the appropriate tool to each model.

---

## Patch Tool (@@-style)

### Format

```diff
@@
 context line
-removed line
+added line
 context line
```

### When to Use

- Your model has been trained with OpenAI's `apply_patch` or similar patch-based interfaces
- You need precise positional control through context lines
- The change is local and can be anchored with surrounding code

### Example

```json
{
  "path": "main.go",
  "patch": "@@\n func main() {\n-\tfmt.Println(\"hello\")\n+\tfmt.Println(\"hello, world\")\n }"
}
```

### Optional Header Anchors

You can add a header line to help locate ambiguous blocks:

```diff
@@ func processUser(id int) error {
 if id < 0 {
-  return errors.New("invalid")
+  return fmt.Errorf("invalid user ID: %d", id)
 }
```

**Important**: Only use headers you've verified exist in the file. Generic headers (e.g., `@@` alone) are treated as soft anchors and fall back to body matching if not found.

### Error Messages

- **"hunk not found"**: Context lines don't match current file state. Re-read the file and regenerate the patch.
- **"patch makes no changes"**: All lines are context (space prefix); add `-` and `+` lines for actual changes.
- **"multiple possible locations"**: The hunk body matches multiple places. Add more context or use a header anchor.

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

- **Patch**: Better when you need positional control (e.g., "change the first occurrence in this function").
- **Edit**: Better for simple find-replace with clear boundaries.

### For Renaming/Refactoring

- **Edit with `replace_all: true`**: Rename a variable across one file.
- **LSP tool**: For symbol-aware renames across multiple files.

### For Large-Scale Changes

Neither tool is ideal for:

- Creating new files → Use **Write**
- Deleting files → Use **Delete**
- Batch text replacements across many files → Use **Shell** with `sd` or `sed`
- Symbol renames across files → Use **LSP**

---

## Permissions

Both tools share the **file permission family** (path-based authorization). A single approval for a path applies to both editing tools.

### Permission Configuration

Configure permissions for either edit tool name; a rule for one editor applies to the other editor unless the other editor also has an explicit rule:

**Unified Configuration** (recommended):

```yaml
permission:
  edit: allow  # Both patch and edit tools allowed
```

**Disable One Format** (advanced):

```yaml
permission:
  edit: allow
  patch: deny  # GPT/o-series models fall back to edit
```

**Permission Fallback Rules**:

- If only `edit` is configured, `patch` inherits the same permission
- If only `patch` is configured, `edit` inherits the same permission
- This includes `deny`: `edit: deny` disables `patch` too unless `patch` also has its own explicit rule
- If both are configured, each tool uses its own explicit rule
- A single `edit` or `patch` rule applies to both tools and overrides wildcard rules

**Examples**:

- `edit: allow` → both tools allowed; GPT/o-series models normally see `patch`, other models normally see `edit`
- `edit: allow, patch: deny` → patch denied, edit allowed; GPT/o-series models fall back to `edit`
- `patch: allow, edit: deny` → patch allowed, edit denied; non-GPT models fall back to `patch`
- `*: deny, patch: allow` → both tools allowed (patch rule is inherited by edit)
- `*: allow, patch: deny` → both tools denied (edit inherits patch deny)

---

## Technical Notes

### Why Two Tools?

Models exhibit strong preferences based on their training data:

- GPT models have seen extensive `@@`-style patches in their training (OpenAI's `apply_patch`)
- Claude, Qwen, and similar models perform better with intuitive find-replace formats

Empirical testing (Aider's edit-bench, internal chord metrics) shows:

- **GPT models**: 91-96% success with patch format, ~70% with replace
- **Non-GPT models**: 81-96% success with replace format, 44-79% with patch

### Token Efficiency

- **Replace**: Generally 20-40% fewer tokens for small edits (no context lines required)
- **Patch**: More tokens due to context, but better precision for complex edits

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
   - `codex` provider → `patch` tool
   - Other providers → `edit` tool
3. **Permission continuity**: Both tools share the file permission family

---

## FAQ

**Q: Can I force a specific tool?**
A: The tool selection is automatic and model-specific. Overriding it may reduce success rates.

**Q: What if my model isn't recognized?**
A: By default, unrecognized models use the `edit` (replace) tool. GPT/o-series models use `patch`.

**Q: Do both tools support the same file types?**
A: Yes. Both work with any text file (detected encoding: UTF-8, UTF-16, GB18030, etc.). Binary files are rejected.

**Q: Can I use both tools in the same conversation?**
A: Only one tool is visible at a time, based on the active model. You won't see both simultaneously.

**Q: What about `hashline` (content-addressed anchors)?**
A: Not currently enabled. It's a potential future enhancement after validation in production workloads.

---

## See Also

- [Tool Reference](./tools.md) – All available tools
- [Permission System](./permissions.md) – How file access control works
- [LSP Integration](./lsp.md) – Symbol-aware operations
