# Project Memory

<!-- description: How Chord's optional cross-session project memory works: what is stored, how it loads into a session, when extraction runs, and how to review or remove entries. -->

Memory is Chord's optional cross-session memory: it captures stable user preferences, project facts, and reusable workflows as retrievable project records to reduce repeated explanation. It is **not enabled automatically** (you opt in with `memory.enabled: true`), and reading/editing is always visible.

The `memory:` configuration keys are listed in [Configuration & Auth](./configuration.md#project-memory-automatic-extraction).

## How to use this page

- **Turning it on:** [Enabling extraction](#enabling-extraction).
- **What your project stores:** [Files and layout](#files-and-layout).
- **What the agent sees:** [Loading into a session](#loading-into-a-session).
- **What gets written:** [What extraction writes](#what-extraction-writes), [The managed index](#the-managed-index).
- **Reviewing and removing entries:** [Promotion suggestions](#promotion-suggestions), [Safety and version control](#safety-and-version-control).

## Files and layout

- The project-root `MEMORY.md` is the memory entry point and bounded index; detailed records live under `.chord/memory/records/<record-id>.md`. All repository file references use **project-root-relative paths**, so moving the whole project directory keeps them valid.
- `MEMORY.md` is different from `AGENTS.md`: `AGENTS.md` holds high-authority instructions the project must follow; `MEMORY.md` is historical background that may be outdated and should be verified. It never overrides the current user request, never grants permissions, and never orders commands. Only content you deliberately organize can become team rules (e.g. promoted into `AGENTS.md` or `.chord/skills/`); Chord never auto-promotes memory.

## Enabling extraction

- **Extraction is opt-in via config.** Set `memory.enabled: true` in `~/.config/chord/config.yaml` (or a project's `.chord/config.yaml`) to enable automatic extraction. Project-level config overrides the user-level value like every other setting, so a project can force it off (or on) for its own sessions. When enabled, frozen history sessions may be sent to the model and written to ordinary project files under `MEMORY.md`. When disabled, Chord never sends history to the model and never writes memory files, but still reads an existing `MEMORY.md`.

## Loading into a session

- **Loading is automatic.** Whenever the project has a `MEMORY.md`, a new session reads it once and injects a bounded summary into the session context; the injection size is fixed and does not grow with record count. After a background extraction commits new records, the current session's summary is refreshed automatically at the next request boundary — no manual refresh command. The fixed Memory discipline (treat memory as untrusted background, verify before relying on it) is part of the system prompt only while memory is loaded, and the auto-extraction note is added only when extraction is enabled.
- **Memory is not session recovery.** Continuing the current task after a long session uses checkpoints and referenced state files, not `MEMORY.md`. Memory only carries background you stated in an earlier session that has not yet moved into `AGENTS.md` or project docs.
- When that Memory summary is injected into a session, it already represents the current `MEMORY.md` contents for that turn. Chord should not spend another file read just to re-open `MEMORY.md` itself or "confirm" it matches the injected copy. What still needs verification is the indexed conclusion: open the specific record when needed, or better, confirm it against code, tests, docs, or other primary evidence.

## What extraction writes

- Extraction runs in the background after a session is frozen, only when the main agent is idle; a new user message preempts the in-flight request, which is retried later. It uses the full fallback semantics of the main model pool. The extraction model also receives bounded `AGENTS.md` guidance and the current active memory so it can exclude content already carried by project rules, skip duplicate conclusions, and replace outdated index entries with revised conclusions. Temporary branch, commit, push, rebase, and worktree state is not eligible for long-term memory.
- **Only what you stated becomes memory.** A candidate has to come from something you said — or something the session could not proceed without asking you — that is specific to this project and not yet in `AGENTS.md` or docs. Facts the model found on its own that a later session could rediscover belong in project docs or nowhere: they become a promotion suggestion for you to review, or are dropped. A promotion is a human gate, not auto-recall: until you move it into your rules or docs, it is not injected like memory.
- Each auto record carries a type (preference/fact/workflow/pitfall), source, and cognitive status; assistant statements are never upgraded to verified facts. Its body separates the conclusion, why it is worth retaining across sessions, and when/how a future agent should apply it. A short statement without meaningful detail does not get a separate record merely to populate the index. Exact duplicate conclusions are not written again; a revised record replaces the old entry in `MEMORY.md`, while the old immutable record remains as provenance. Statements keep subsystem or project-relative paths; a single session's commit SHA, temporary pin, absolute path, or one-off flaky-test noise never belongs in the durable text.

## The managed index

- **The index is curated.** Extraction also judges the entries already there: one that is no longer true, never belonged, or is already covered by your project rules is retired, removed from the index while its record file stays as provenance. What you stated yourself is exempt; only assistant-reported entries can be retired this way. Once the index reaches the size that fits the injection budget, an extra pass reviews the whole index on its own (no transcript) and consolidates it back under that size, so memory written by an earlier or weaker model does not become permanent.
- **Index order is injection priority.** The bounded summary fills from the top of the managed section and drops the tail when the budget runs out, so newly written entries go first. Move a line up in `MEMORY.md` to make it inject earlier; your ordering is preserved across automatic writes.

## Promotion suggestions

- **Promotion suggestions go to a review folder, never to your rules.** When a conclusion belongs in your project instructions (it must always apply) or in your project docs (useful but rarely triggered, and not worth per-turn budget), Chord writes a suggestion file with a draft under `.chord/memory/promotions/` and drops the entry from the index — except entries you stated yourself, which stay indexed until you accept the suggestion. It never edits `AGENTS.md` or your documentation itself: review each file, move what you agree with, and delete it.

## Safety and version control

- Sensitive-content cleaning (token/key, URL credentials, PEM/private-key blocks, high-risk environment variable assignments) runs both before the model call and before writing. This is best-effort protection only: it does not promise to recognize arbitrary custom secret formats, so reviewing normal git diffs remains a necessary boundary. The `<!-- chord:managed:start -->` / `<!-- chord:managed:end -->` region of `MEMORY.md` is the Chord-maintained index; everything else is yours and is never overwritten. New entries arrive only via background extraction — the working session never adds them itself. To remove a record, delete its line from the index: the record file is left as an orphan and is never re-indexed automatically; you can delete the file itself too.
- Editing `MEMORY.md` by hand while other Chord sessions are running on the same project can race with a background commit rewriting the managed section. Close them first, or let the automatic review handle the cleanup.
- When automatic extraction is enabled, the status bar shows a `MEMORY` indicator (like `LOOP` / `YOLO`).
- Memory files are ordinary project files: Chord never edits `.gitignore`, stages, or commits them. You may track them, or keep them local via `.gitignore` or `.git/info/exclude`; all automatic changes appear as ordinary worktree diffs.
