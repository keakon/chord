# Changelog

This project follows Semantic Versioning-style releases. Before 1.0, releases may include breaking changes.

## Unreleased

- Fixed TUI transcript mouse selection copy so drag-selected text keeps the final character when copied with `Cmd+C`, and documented transcript copying behavior.
- Improved loop verification continuations so `verify` assessments inject a dedicated `LOOP VERIFY` notice with explicit verification guidance, and documented `/loop on [target]`.
- Fixed LSP sidebar diagnostics so clean post-edit self-reviews persist `0E/0W` snapshots and clear stale errors after syntax issues are resolved.
- Fixed TUI card background artifacts around emoji, variation selectors, and ZWJ graphemes by aligning wrapping, padding, and truncation with the viewport grapheme-width calculation.
- Improved TUI tool-call file path display so local-path tools such as `Read`, `Write`, `Edit`, `Delete`, `Grep`, and `Glob`, along with existing visible path-bearing Bash metadata, show paths relative to the active project root when possible, including after session resume/startup restore and spill/hydrate recovery, while keeping external paths absolute.
- Improved AGENTS.md handling by adding stable system-prompt framing only when repository guidance exists, while keeping AGENTS.md contents in the session `<system-reminder>` context layer.
- Fixed sticky fallback model variants so pinned fallback requests preserve their own `@variant` and do not leak the primary model variant into variantless fallback runs.
- Fixed categorized loop blocked messages rendering as unnamed status cards.
- Fixed Ghostty/tab focus-restore artifacts by upgrading the delayed post-focus redraw into a strong host redraw (`ClearScreen + RequestWindowSize + redraw-settle`), so late stale cells are actively repaired instead of waiting for a later incidental repaint.
- Improved queued tool call badges so they keep right-side padding and hide when the tool header is too narrow.
- Improved TUI markdown rendering caches for assistant/thinking streams, compaction summaries, and status cards.
- Fixed collapsed tool result hidden-line counts for markdown-like output.

## 0.1.0 - 2026-04-29

- Initial public release of Chord.
- Added the terminal coding agent with local-first runtime, Vim-like navigation, session management, model/provider configuration, tool execution, LSP integration, image input, and headless control support.
- Added cross-platform release builds for macOS, Linux, and Windows.
