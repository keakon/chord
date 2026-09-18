# Choosing models

<!-- description: Decide what to connect to Chord, then split models by role. Recipes and examples show how to wire a flagship after you have chosen one. -->

> Snapshot: September 2026. Plans change; confirm on the vendor's site before you buy or reconfigure.

Start here if you still need to choose a channel. Copy-paste wiring is in [Model configuration recipes](./model-configs.md) and [Examples](./examples/index.md). Those snippets use a current flagship so every field is filled in; swap the model IDs after you decide.

## Will what you already pay for reach Chord?

Chord accepts an API key, or Codex OAuth (`chord auth codex`). A subscription that only signs you into that vendor's own app does not.

| You already have | In Chord? |
| --- | --- |
| A coding plan that supports third-party clients (for example ChatGPT Plus / Pro (Codex), Command Code GOAT, OpenCode Go) | Yes |
| An API key for any provider Chord speaks (Chat Completions, Responses, Messages, Generate Content) | Yes |
| Claude Pro / Max, SuperGrok, Muse Code, Google AI Pro / Ultra, or another vendor-only coding plan | No |
| GLM Coding Plan | No (only in the tools and products GLM officially supports) |

## Four ways to decide

### 1. You already subscribe to something

If the table says Yes, use it. If it says No, leave that app alone and continue with case 2, 3, or 4. On Codex, one plan fills all five roles: orchestrator, explorer, and coder on the cheapest model your account lists; expert and reviewer on the strongest model the plan can sustain. For a cheap plan or an API key, follow case 2.

### 2. Light use, and you want value

Get a cheap official API key (for example Gemini 3.8 Flash, DeepSeek V4.1 Flash, or GPT-5.6 Luna), or a cheap plan that exposes a standard endpoint (for example Command Code GOAT or OpenCode Go). Use it for routing, search, most edits, and all five roles. Upgrade expert and reviewer only when hard decisions become common; if the cheap plan does not carry a strong enough model, add a separate official key for them.

### 3. Heavy use

A flat subscription beats per-token billing when you use it every day. Subscribe to Codex and sign in: orchestrator, explorer, and coder on the cheapest model your account lists; expert and reviewer on the strongest model the plan can sustain.

### 4. Bill size is not the issue

Give the strongest API models you can get (GPT-6 Astra and Claude Fable 5.1 as of this snapshot) to expert and reviewer. Keep a cheap fast model on orchestrator, explorer, and coder; they do not need a flagship.

## Which model goes to which role?

Routing and mechanical edits do not need a flagship; a cheap model handles them. Split pools by job: one for cheap, fast models, one for the flagship you will pay for; the team example calls them `fast` and `deep`.

The names below are not built-in. Chord ships `builder` and `planner`. The five-role split is the optional [team example](./examples/examples-team.md). Copy it if you want that layout.

| In the team example | Suggested model | Because |
| --- | --- | --- |
| orchestrator | Gemini 3.8 Flash, DeepSeek V4.1 Flash, GPT-5.6 Luna | It classifies, dispatches, and synthesizes every turn. |
| explorer | DeepSeek V4.1 Flash, Gemini 3.8 Flash, GPT-5.6 Luna | Read-only scouting; it reports where files are and makes no judgment calls. DeepSeek is cheapest, Gemini reads material better. |
| coder | DeepSeek V4.1 Flash, Gemini 3.8 Flash, GPT-5.6 Luna | What to change and how is already written down; mechanical edits are within reach of any of them. |
| expert | Claude Fable 5.1, GPT-6 Astra | Root cause, architecture, concurrency, and hot paths; a wrong call becomes hidden debt. |
| reviewer | Claude Fable 5.1, GPT-6 Astra | Catches regressions and invariant breaks; it does not redesign. |

If you only have a subscription and no separate API keys, pick the same two levels from that plan's catalog: the cheapest model for orchestrator, explorer, and coder, and the strongest the plan can sustain for expert and reviewer. On Codex that is GPT-5.6 Luna and GPT-6 Astra (use GPT-5.6 Sol when the account does not include Astra).

In practice, split pools by job: `deep` holds the expert and reviewer models and `fast` holds the explorer and coder models. The team example keeps orchestrator on `deep`; in real use it needs no flagship, so `fast` or its own cheap pool works too.

### Which model for retrieval?

- **Files and code inside the repo**: DeepSeek V4.1 Flash. Read-only scouting does not need closed-book knowledge, and its unit price and cache are the cheapest, which suits re-reading the same files.
- **Web material, PDFs, charts**: Gemini 3.8 Flash. Long PDFs and charts are where it is strongest; DeepSeek is weak closed-book, so retrieval has to come from a search tool rather than its memory.
- **Codex subscription only**: use GPT-5.6 Luna for repo scouting; its long context is weak, so narrow the range first on very large repos.
- **Need both and want a single model**: use Gemini 3.8 Flash.

### Can coder use a cheap model?

Yes, and it should by default. Coder is for changes that are already decided: renames, mechanical refactors, format and config updates, small local fixes, tests behind a fixed interface. Judgment stays with expert, and for this kind of work DeepSeek V4.1 Flash, Gemini 3.8 Flash, and GPT-5.6 Luna are all enough, for far less money than a flagship.

It is not for work that still needs judgment: an open "why" or "which approach", a change to protocol, data models, concurrency or lifetimes, permissions, or recovery, or a task that has already failed twice. System-level work in an unfamiliar environment (new language, new build system, inside a container) needs its path and acceptance criteria pinned down first, and of the three, DeepSeek V4.1 Flash is the weakest there.

### No GPT or Claude subscription — what should expert use?

Neither vendor requires a subscription: both sell API keys, so Fable 5.1 and Astra stay on the table without a ChatGPT or Claude plan. If you want to stay off both vendors entirely, start at item 3.

1. **Claude Fable 5.1**: pay as you go with an Anthropic API key. It is strongest at architecture taste, root-cause analysis, and deep research, and its cache pricing suits re-reading the same files.
2. **GPT-6 Astra**: buy an OpenAI API key on its own. It spends fewer tokens, which suits a narrowed question that has to be settled in one pass; its cache is pricier, so do not feed it the whole repo every turn.
3. **Muse Spark 1.3** (Meta Model API): the strongest model outside the GPT and Claude channels. Long-horizon implementation and large-repo work are its strengths; its root-cause and architecture judgment is a notch lower, so split expert work smaller and verify more.
4. **GLM-5.3 or Kimi K3**: the strongest open models, carried by open-model plans such as OpenCode Go and Command Code GOAT. They can hold expert work, but not the final word on architecture or concurrency.
5. **None of these**: keep the expert question small (have a cheap model reproduce it and narrow the range) and revisit the paid options above when a wrong call would become hidden debt. Gemini 3.8 Flash can hold a first discussion; do not let it be the final reviewer.

Reviewer runs the same model as expert. With only one flagship budget, give it to expert first and open reviewer after a substantial change.

### What does orchestrator need?

It runs every turn: read the task, classify it, dispatch, collect results, decide between correcting and escalating. It needs to:

- call tools reliably and read a worker's report well enough to restate the conclusion;
- tell whether a task still needs a product decision: if yes, send expert; if the path and the replacement are already written down, send coder; if it is only about where files are, send explorer;
- stay cheap and fast. Flagship reasoning and taste are wasted here; the only risk worth paying to avoid is misrouting.

Default to Gemini 3.8 Flash, DeepSeek V4.1 Flash, or GPT-5.6 Luna; DeepSeek is the cheapest of the three. Upgrade only if you observe frequent misrouting; do not start on a flagship.

## After you decide

1. Copy the matching snippet from [Model configuration recipes](./model-configs.md).
2. For a full file layout, start from [Examples](./examples/index.md) and replace the flagship IDs with what you actually picked.
3. Confirm with `chord doctor models`.
