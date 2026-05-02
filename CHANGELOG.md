# Changelog

This project follows Semantic Versioning-style releases. Before 1.0, releases may include breaking changes.

## Unreleased

- Replaced the previous `slog`-style logging adapter with direct `golog` usage across the runtime. Logs are now plain golog text output with direct caller attribution; the previous pseudo-structured `level=... msg=... key=value` formatting and default logger `With(...)` context fields are no longer emitted automatically.
- Fixed `ee`/fork editing for image messages so images restored from session history by path are reloaded and included when the edited user message is sent again.
- Fixed TUI tool-card rendering so tool arguments/results are shown as terminal-safe plain text: ANSI/control sequences are escaped, Markdown-looking generic tool output is no longer auto-rendered as Markdown, large collapsed Bash results avoid wrapping hidden tails, and collapsed hidden-line hints no longer double-count the first hidden line.
- Removed obsolete pre-1.0 compatibility paths and dead code across compaction, LLM session handling, LSP test-only helpers, tools, and TUI internals. This cleanup deletes the unused `ResetResponsesSession` / legacy responses-session reset chain, removes the old synchronous compaction fallback path, moves test-only helpers into `_test.go`, deduplicates fallback-summary rendering, finishes centralizing tool-name handling on `tools.NameXxx` constants, and keeps plan-execution session switches aligned with the active provider/session identifier lifecycle.
- Fixed long-session TUI transcript clipping where late updates to older background status cards could undercount transcript height after spill/hydrate recovery, making the bottom rows or even several final cards unreachable.
- Removed the deprecated `blockers_remaining` field from the SubAgent `Complete` tool arguments and `CompletionEnvelope`; SubAgents must report non-blocking caveats via `remaining_limitations` and signal true blockers through the Escalate or `blocked` mailbox path instead of `Complete`.
- Unified SubAgent artifact representation: mailbox messages, durable task records, per-instance meta files, and the in-memory runtime state now reference artifacts via a single `ArtifactRef` / `[]ArtifactRef` shape; removed the parallel `artifact_ids` / `artifact_rel_paths` / `artifact_type` fields and the related legacy adapter.
- Replaced remaining hard-coded tool-name string literals (`Read` / `Write` / `Edit` / `Delete` / `Grep` / `Glob`) across TUI rendering, search, hooks, agent execution paths, and edit tracking with the centralized `tools.NameXxx` constants.
- Removed the unused deprecated `skill.Loader.Scan()` helper; remaining callers already use `ScanMeta` plus on-demand `Load`.
- Improved MCP initialize handshake metadata so runtime-managed MCP clients now send the build-time Chord version instead of a stale hardcoded version, while preserving the default `mcp.NewClient` / `NewPendingManager` / `NewManager` convenience APIs and adding explicit `WithClientInfo` variants for callers that need custom handshake identity.
- Centralized local tool traits used by TUI expansion and compaction (`Read` / `Grep` / `Glob` / `WebFetch` vs file-mutating tools) into `internal/tools/tool_traits.go` to reduce scattered string-driven branching.
- Removed the legacy `ProviderConfig.UpdatePolledRateLimitSnapshot` test-compat wrapper in favor of explicit credential-index updates via `UpdatePolledRateLimitSnapshotForCredentialIndex`.
- Added structured SubAgent completion handoff with files changed, verification run, remaining limitations, risks, follow-up recommendations, and artifact references.
- Fixed TUI tool cards so queued badges and wrapped content keep consistent right-side padding.
- Added session-scoped `SaveArtifact` and `ReadArtifact` tools for SubAgent handoff artifacts, with persistence through mailbox, task registry, snapshots, and session restore.
- Improved SubAgent coordination snapshots to surface recent completion metadata, artifact references, write scope, and suspected stalls.
- Fixed transcript selection column handling for tab-expanded rendered lines.
- Fixed TUI transcript mouse selection copy so drag-selected text keeps the final character when copied with `Cmd+C`, and documented transcript copying behavior.
- Improved loop verification continuations so `verify` assessments inject a dedicated `LOOP VERIFY` notice with explicit verification guidance, and documented `/loop on [target]`.
- Fixed LSP sidebar diagnostics so clean post-edit self-reviews persist `0E/0W` snapshots and clear stale errors after syntax issues are resolved.
- Fixed TUI card background artifacts around emoji, variation selectors, and ZWJ graphemes by aligning wrapping, padding, and truncation with the viewport grapheme-width calculation.
- Improved TUI tool-call file path display so local-path tools such as `Read`, `Write`, `Edit`, `Delete`, `Grep`, and `Glob`, along with existing visible path-bearing Bash metadata, show paths relative to the active project root when possible, including after session resume/startup restore and spill/hydrate recovery, while keeping external paths absolute.
- Improved AGENTS.md handling by adding stable system-prompt framing only when repository guidance exists, while keeping AGENTS.md contents in the session `<system-reminder>` context layer.
- Fixed sticky fallback model variants so pinned fallback requests preserve their own `@variant` and do not leak the primary model variant into variantless fallback runs.
- Fixed categorized loop blocked messages rendering as unnamed status cards.
- Fixed Ghostty/tab focus-restore artifacts by tracking transcript/layout changes that occur while the terminal is backgrounded, forcing a host redraw after focus-settle when those background changes are observed, and recording background-dirty plus input-separator diagnostics to make any remaining stale-display cases easier to investigate.
- Fixed Ghostty/cmux stale display where separator lines could appear duplicated after rapid scroll/resize/layout changes; Chord now clears to end-of-line per rendered row in those terminals to avoid leftover cells.
- Improved queued tool call badges so they keep right-side padding and hide when the tool header is too narrow.
- Improved TUI markdown rendering caches for assistant/thinking streams, compaction summaries, and status cards.
- Fixed collapsed tool result hidden-line counts for markdown-like output.
- Fixed headless idle events so Chord emits a single `idle` envelope instead of also sending a duplicate ready `notification` envelope; gateways should render the idle state themselves.

## 0.1.0 - 2026-04-29

- Initial public release of Chord.
- Added the terminal coding agent with local-first runtime, Vim-like navigation, session management, model/provider configuration, tool execution, LSP integration, image input, and headless control support.
- Added cross-platform release builds for macOS, Linux, and Windows.
