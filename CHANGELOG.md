# Changelog

This project follows Semantic Versioning-style releases. Before 1.0, releases may include breaking changes.

## Unreleased

- Improved AGENTS.md handling by adding stable system-prompt framing only when repository guidance exists, while keeping AGENTS.md contents in the session `<system-reminder>` context layer.
- Fixed sticky fallback model variants so pinned fallback requests preserve their own `@variant` and do not leak the primary model variant into variantless fallback runs.
- Fixed categorized loop blocked messages rendering as unnamed status cards.
- Fixed Ghostty focus restore artifacts by keeping the late post-focus redraw after weak streaming boundary redraws.
- Improved queued tool call badges so they keep right-side padding and hide when the tool header is too narrow.
- Improved TUI markdown rendering caches for assistant/thinking streams, compaction summaries, and status cards.
- Fixed collapsed tool result hidden-line counts for markdown-like output.

## 0.1.0 - 2026-04-29

- Initial public release of Chord.
- Added the terminal coding agent with local-first runtime, Vim-like navigation, session management, model/provider configuration, tool execution, LSP integration, image input, and headless control support.
- Added cross-platform release builds for macOS, Linux, and Windows.