# Choosing models

<!-- description: Choose concrete models by task difficulty, then assign coordination, implementation, and review roles. Includes four task tiers and team recipes. -->

> Selection date: October 9, 2026. Candidates are listed in suggested trial order, not as a Chord benchmark ranking. Confirm official model names, prices, and account availability before buying or reconfiguring.

Start with the balanced tier for everyday development and the reliability-first tier for concurrency, permissions, or recovery. Use cheaper models for explicit mechanical work. A single main agent can finish a small task without starting a team. Add models with [`chord config add`](./cli.md#chord-config-add); the catalog supplies limits, modalities, and reasoning variants. Full layouts are in [Examples](./examples/index.md).

## Will what you already pay for reach Chord?

Chord accepts an API key, or Codex OAuth (`chord auth codex`). A subscription that only signs you into that vendor's own app does not.

| You already have | In Chord? |
| --- | --- |
| A coding plan that supports third-party clients (for example ChatGPT Plus / Pro (Codex), Command Code GOAT, OpenCode Go) | Yes |
| An API key for any provider Chord speaks (Chat Completions, Responses, Messages, Generate Content) | Yes |
| Claude Pro / Max, SuperGrok, Muse Code, Google AI Pro / Ultra, or another vendor-only coding plan | No |
| GLM Coding Plan | No (only in the tools and products GLM officially supports) |

If you already have a supported plan, start with models your account actually exposes. Whether a subscription saves money depends on its quotas, throttling, and your workload. API costs also include retries, review, and rework. With a limited strong-model budget, prioritize decisions that affect the whole task instead of allocating it by role name alone.

## Choose a task tier

These tiers are configuration recommendations. Chord does not switch between them based on task difficulty. Price tiers are not capability tiers, and one model can serve several roles.

### Economy: explicit, local, easy to verify

Use this tier to locate files and callers, apply fixed renaming rules, adjust configuration, or add tests against a settled interface.

1. GPT-6 Luna: structured extraction, mechanical execution, and dispatch against a settled plan.
2. DeepSeek V4.1 Flash: repository discovery, repeated reads, and code work with explicit specifications.
3. Gemini 3.8 Flash: move it first for material containing PDFs, charts, or screenshots.

A coordinator in this tier needs a settled goal, dependencies, and acceptance criteria. Escalate when it must decompose the requirement again, choose behavior, or resolve design disagreements. Root-cause analysis and security-sensitive decisions are not mechanical work.

### Balanced: everyday development

Use this tier for ordinary features, multi-file edits, reproducible bugs, and refactoring whose interfaces are mostly settled.

1. GPT-6.1 Sol: a starting choice for the daily main agent and implementation. Start at `medium`; try `high` for harder planning or review.
2. Claude Sonnet 5.5: a candidate for the everyday coder or main agent. Start implementation at `high`, then test whether lower effort preserves quality.
3. Claude Opus 5.5: use for coordination, implementation, or review when requirements are less clear, call chains are longer, or rework is expensive.

The coder can choose local implementation details. Changes to behavior, public interfaces, or security boundaries still need owner agreement. Each edit need not pass through an explorer, expert, coder, and reviewer in sequence.

### Reliability-first: complex repositories and high-risk changes

Use this tier for cross-subsystem changes, intermittent failures, concurrency and lifetimes, permissions, durable recovery, and performance hot paths.

1. Claude Opus 5.5: the default candidate for coordination, difficult implementation, and review; try `high` first.
2. GPT-6 Astra: invoke for important design decisions, difficult root causes, or an independent investigation.
3. GPT-6.1 Sol: a cost-conscious candidate starting at `high`. Validate it on project tasks before expanding its responsibilities.

An expert may investigate, implement, and verify a difficult part directly. Forcing a handoff to a cheaper coder before the implementation is unambiguous can add handoff and rework costs.

### Intensive: stalled approaches or costly failures

First check whether the failure comes from the environment, a false assumption, or unclear acceptance criteria before spending more on model capability.

1. GPT-6 Astra: give it a complete difficult investigation or design task.
2. Claude Fable 5.1: use for difficult reasoning or a second independent investigation.
3. Claude Opus 5.5: raise effort for a new approach supported by evidence. A more expensive model does not guarantee a better result.

Keep the ordinary coder and explorer; reserve intensive models for the difficult parts. When a second opinion is needed, let the investigations gather evidence independently before comparing conclusions.

## Team roles and model pools

Chord ships `builder` and `planner`. The roles below come from the optional [team example](./examples/examples-team.md); create only those you need. The same model can serve multiple independent sessions.

| Role | Default pool | Responsibility |
| --- | --- | --- |
| orchestrator | `deep` | Preserve the full requirements, decompose work, coordinate design, correct course, and accept the whole deliverable. Handle small tasks directly. |
| coder | `coding` | Implement and verify bounded changes; report unsettled behavior or interfaces. |
| explorer | `fast` | Locate files, call relationships, and tests; return traceable evidence and uncertainty. |
| reviewer | `deep` | Review independently against original requirements and actual changes; check regressions and verification gaps. |
| expert | `deep`, with an intensive model when needed | Handle root causes, design, and difficult implementation; finish a specialty task directly when useful. |

### How capable must the coordinator be?

A dispatcher executing a settled plan can use a validated midrange or lightweight model. An open-ended team lead also has to detect missing constraints, judge dependencies, reconcile conflicting reports, and decide when work is complete. Establish its baseline with the balanced or reliability-first tier.

Having an expert does not remove the coordinator's judgment work. The expert needs the relevant goals and constraints and must be able to report conflicts between the plan and the evidence. The coordinator must preserve caveats instead of turning “local tests passed” into “the whole task is complete.”

### Three starting recipes

| Recipe | `deep` | `coding` | `fast` |
| --- | --- | --- | --- |
| Economy, explicit specifications | GPT-6.1 Sol | GPT-6 Luna | GPT-6 Luna |
| Everyday development | GPT-6.1 Sol | Claude Sonnet 5.5 | GPT-6 Luna |
| Complex development | Claude Opus 5.5 | Claude Sonnet 5.5 | GPT-6 Luna |

The [team example](./examples/examples-team.md) uses the complex-development recipe. With Codex alone, choose Sol from the account catalog for coordination, implementation, and review, and Luna for discovery and mechanical work; use Astra for intensive work when available. With only an Anthropic API key, Sonnet can cover everyday implementation and discovery while Opus handles difficult decisions. API keys do not require a subscription to the vendor's chat app.

Replace the discovery model with DeepSeek V4.1 Flash or Gemini 3.8 Flash when the task suits it. For other providers, apply the same role criteria and run representative project tasks before assigning coordination or final review.

Multiple entries in a model pool provide fallback on request failures. They do not select by task difficulty or escalate because an answer is wrong. Choose the appropriate role or pool explicitly when changing tiers; a pool containing all three candidates is not a task router.

## Tuning and verification

- Compare a single agent, a strong coordinator with midrange workers, and a midrange coordinator with the same workers on the same real tasks. Change one factor at a time and repeat close comparisons.
- Record end-to-end acceptance, false completion, human corrections, total cost, and elapsed time. Token prices exclude the effect of retries, review, and rework.
- Start with few workers. Parallel writes need independent deliverables and settled shared decisions; different files can still have semantic dependencies. `expected_write_scope` is a coordination declaration, while role rules determine actual permissions.
- Review original requirements, actual changes, and verification evidence, not just the implementer's summary. Different models offer another perspective but do not guarantee independent errors.
- Start with supported `medium` or `high` effort and adjust from results. Effort labels across vendors do not represent equal compute, and `max` does not guarantee better cost or quality.
- Run `chord doctor models --pool <pool-name>` to check connectivity. It does not measure task success. Use Responses for GPT-6.1 Sol tool tasks; see [Model configuration](./model-configs.md).

## Sources and limits

Use the [OpenAI model docs](https://developers.openai.com/api/docs/models), [Anthropic model docs](https://platform.claude.com/docs/en/models/overview), [Gemini 3.8 Flash model card](https://deepmind.google/models/model-cards/gemini-3-8-flash/), and [DeepSeek V4.1 Flash model card](https://huggingface.co/deepseek-ai/DeepSeek-V4.1-Flash) for model identity and API capabilities. [Artificial Analysis](https://artificialanalysis.ai/models) compares general capability, speed, and cost; its scores do not directly measure Chord coordination success.

Public work from 2026 supports allocating models by responsibility, without establishing a universal coordinator threshold:

- [Cursor's multi-agent engineering experiment](https://cursor.com/blog/agent-swarm-model-economics) demonstrates strong planners with cheaper workers but does not cover every model pairing.
- [The small-agent collaboration study](https://arxiv.org/abs/2601.11327) finds coordinator reasoning important, primarily on question-answering and tool tasks.
- [The software-repair manager/worker experiment](https://arxiv.org/abs/2603.26458) supports the value of strong direction, with limited model pairings and a small weak-coordinator sample.
- [DeOrch](https://arxiv.org/abs/2610.07556) studies a specially trained small coordinator. Its results do not transfer directly to cheap general-purpose models without orchestration training.

These sources inform candidate configurations. Validate any downgrade against your own repository and acceptance criteria.
