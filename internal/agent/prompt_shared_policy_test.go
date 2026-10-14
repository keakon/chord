package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestSharedCodingGuidelinesPrompt_ExcludesMainAgentOnlyCommunicationGuidance(t *testing.T) {
	got := sharedCodingGuidelinesPrompt
	for _, unwanted := range []string{
		"before substantial work",
		"discover a root cause",
		"change direction",
		"complete a key implementation or verification step",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sharedCodingGuidelinesPrompt should not contain MainAgent-only communication guidance %q in %q", unwanted, got)
		}
	}
	for _, want := range []string{
		"Before claiming completion, compare the requested behavior with the implementation and verification already gathered",
		"For analysis, review, or planning tasks",
		"begin with repository evidence: relevant code, existing tests, CI configuration, documentation, and history",
		"Install dependencies or run dynamic checks only when requested or necessary to support a material conclusion",
		"state remaining runtime uncertainty",
		"For implementation and bug-fix tasks, verify the requested behavior",
		"Passing tests do not dismiss an unchecked counterexample",
		"When a check you wrote for the requested behavior fails, fix the code or show from the requirements that the check is wrong",
		"changing its inputs so the failing path no longer runs is not a fix",
		"When a candidate fix is cheap to try, run it instead of deliberating about whether it would work",
		"check it with the smallest reproduction before adding coverage or running broader tests",
		"state verification status explicitly (passed, failed, not run, or only inspected statically)",
		"following project-local test/build conventions when known",
		"Do not narrate every routine action, restate obvious next steps, or over-explain — lead with the action or answer",
		"If multiple interpretations exist but one is clearly the best fit",
		"Before implementing new logic, look for existing helpers and patterns.",
		"Reuse them when they satisfy the requested behavior, including libraries the repo already depends on",
		"\"no new dependencies\" forbids adding packages, not using existing ones",
		"Do not introduce parallel helpers or duplicate logic when an existing local abstraction can be reused or slightly extended",
		"Do not add error handling, fallbacks, validation, or defensive checks for scenarios that cannot happen",
		"only validate at real trust boundaries",
		"three similar lines is better than a premature abstraction",
		"only comment a non-obvious WHY",
		"Do not leave backwards-compatibility shims",
		"Remove imports, variables, and functions that your own changes made unused",
		"Do not remove pre-existing dead code unless asked",
		"Keep necessary callers, fixtures, tests, accessibility, security, compatibility, and migration work when reachable evidence requires it",
		"fewer files or lines is not the goal — the smallest correct result is",
		"Do not introduce new abstractions, helper layers, configuration knobs, feature flags, checksums, new third-party dependencies, migrations, compatibility layers, or parameters reserved for hypothetical future needs",
		"Do not add a final audit loop, re-review, or re-test pass only to demonstrate compliance with these rules",
		"state a brief plan with verifiable success criteria per step",
		"For analysis-only tasks, define success in terms of evidence gathered and conclusions supported, not implementation or acceptance-test completion",
		"Run the narrowest check that can falsify the change first",
		"usually run the full suite once at the end, again only when a later change invalidates its result",
		"Repeat a command, search, or failed approach only when something relevant changed since the last attempt (code, inputs, environment, or the hypothesis being tested)",
		"status polling and flaky-failure triage are exceptions",
		"When a search or approach is exhausted, use the available evidence to implement, run a focused experiment, or report a specific blocker",
		"establish that the code builds before running broad tests",
		"A guarantee stated without conditions holds on success, errors, and early returns",
		"Existing code is a reference, not permission to weaken a requirement",
		"Verify the promised state on relevant exit paths",
		"report its other failures of the same kind through that error, keeping the underlying error as the cause",
		"Broaden when the change's impact or project requirements warrant it",
		"When a broad test fails, narrow the reproduction before retrying",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedCodingGuidelinesPrompt missing %q in %q", want, got)
		}
	}
}

// The reasoning-discipline block governs the reasoning trace only. Verification
// depth, completion claims, and narration are owned by the values and coding
// guidelines blocks; asserting both halves keeps every rule single-sourced
// instead of letting a second copy drift away from the original.
func TestSharedReasoningDisciplinePrompt_ContentAndBoundary(t *testing.T) {
	got := sharedReasoningDisciplinePrompt
	for _, want := range []string{
		"## Reasoning Discipline",
		"keep only the next one or two decisions active",
		"use existing task state or notes when they materially help",
		"do not create external artifacts solely for ephemeral thoughts",
		"Reasoning is for decisions, invariants and the checks you will run",
		"not for transcribing the code you are about to write",
		"write the real change in the edit/write call",
		"Evidence before conclusion",
		"state concise evidence or rationale supporting a conclusion",
		"Evidence sufficiency",
		"do not keep expanding the search surface to feel thorough",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedReasoningDisciplinePrompt missing %q in %q", want, got)
		}
	}
	for _, owned := range []struct {
		rule  string
		owner string
	}{
		{"verify claims proportionally to the task and risk", sharedAgentValuesPrompt},
		{"Before claiming completion, compare the requested behavior with the implementation and verification already gathered", sharedCodingGuidelinesPrompt},
		{"state verification status explicitly", sharedCodingGuidelinesPrompt},
		{"Do not narrate every routine action", sharedCodingGuidelinesPrompt},
		{"Do not add a final audit loop", sharedCodingGuidelinesPrompt},
	} {
		if !strings.Contains(owned.owner, owned.rule) {
			t.Fatalf("owning block lost %q, which reasoning discipline deliberately omits", owned.rule)
		}
		if strings.Contains(got, owned.rule) {
			t.Fatalf("sharedReasoningDisciplinePrompt restates %q, which another block owns", owned.rule)
		}
	}
}

// The guidelines audience only changes who receives surfaced decisions and
// necessary questions: main talks to the user, sub reaches the owner agent
// through its coordination tools.
func TestCodingGuidelinesPrompt_RoutesDecisionsByAudience(t *testing.T) {
	main := sharedCodingGuidelinesPrompt
	sub := subAgentCodingGuidelinesPrompt

	if !strings.Contains(main, "surface the open product decisions to the user before implementing") {
		t.Fatalf("main guidelines should surface product decisions to the user: %q", main)
	}
	if strings.Contains(main, "owner agent") {
		t.Fatalf("main guidelines should not mention the owner agent: %q", main)
	}

	for _, want := range []string{
		"surface the open product decisions to the owner agent (through the coordination tools available in this role)",
		"Ask before implementing only when missing information is genuinely blocking, the owner agent or user must choose between materially different outcomes",
	} {
		if !strings.Contains(sub, want) {
			t.Fatalf("sub guidelines missing owner-agent routing %q in %q", want, sub)
		}
	}
	if strings.Contains(sub, "surface the open product decisions to the user before implementing") {
		t.Fatalf("sub guidelines should not route product decisions directly to the user: %q", sub)
	}
}

func TestUserConfirmationPromptBlock_UsesQuestionAvailabilitySpecificBranch(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}

	if got := a.userConfirmationPromptBlock(); !strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("userConfirmationPromptBlock without Question should use plain-text branch, got %q", got)
	}

	a.tools.Register(testQuestionTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
question: deny
`)}
	a.rebuildRuleset()
	if got := a.userConfirmationPromptBlock(); !strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("userConfirmationPromptBlock with denied Question should use plain-text branch, got %q", got)
	}

	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
question: ask
`)}
	a.rebuildRuleset()
	got := a.userConfirmationPromptBlock()
	if !strings.Contains(got, "## Structured User Confirmation") {
		t.Fatalf("userConfirmationPromptBlock with Question: ask should use structured branch, got %q", got)
	}

	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
question: allow
`)}
	a.rebuildRuleset()
	got = a.userConfirmationPromptBlock()
	for _, want := range []string{
		"Structured User Confirmation",
		"Default to making ordinary implementation decisions yourself",
		"the Guidelines section defines when asking the user is justified",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("userConfirmationPromptBlock missing %q in %q", want, got)
		}
	}
}

func TestBuildSystemPrompt_IncludesPermissionSpecificUserConfirmationGuidance(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}

	got := a.buildSystemPrompt()
	if strings.Contains(got, "## Structured User Confirmation") {
		t.Fatalf("buildSystemPrompt unexpectedly included structured question guidance without Question tool: %q", got)
	}
	if !strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("buildSystemPrompt missing plain-text confirmation guidance without Question tool: %q", got)
	}
	for _, want := range []string{
		"Because structured confirmation is unavailable in this tool/permission state",
		"make it easy for a non-implementer to answer",
		"their tradeoffs/risks, and your recommended default",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildSystemPrompt missing permission-specific clarification guidance %q without Question tool: %q", want, got)
		}
	}

	a.tools.Register(testQuestionTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
question: deny
`)}
	a.rebuildRuleset()
	got = a.buildSystemPrompt()
	if strings.Contains(got, "## Structured User Confirmation") {
		t.Fatalf("buildSystemPrompt unexpectedly included structured guidance when Question is denied: %q", got)
	}
	if strings.Contains(got, "## Completion Follow-Up") {
		t.Fatalf("buildSystemPrompt should not inject completion follow-up when Question is denied: %q", got)
	}
	if !strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("buildSystemPrompt missing plain-text branch when Question is denied: %q", got)
	}

	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
question: ask
`)}
	a.rebuildRuleset()
	got = a.buildSystemPrompt()
	if !strings.Contains(got, "## Structured User Confirmation") {
		t.Fatalf("buildSystemPrompt missing structured guidance when Question: ask is configured: %q", got)
	}

	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
question: allow
`)}
	a.rebuildRuleset()
	got = a.buildSystemPrompt()
	if !strings.Contains(got, "## Structured User Confirmation") {
		t.Fatalf("buildSystemPrompt missing structured guidance when Question tool is permitted: %q", got)
	}
	if strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("buildSystemPrompt should not keep plain-text branch when Question tool is permitted: %q", got)
	}
}

func TestLoopCompletionRequirementLines_KeepDoneToolContractEvenWhenQuestionAvailable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()
	joined := strings.Join(a.loopCompletionRequirementLines(), "\n")
	for _, want := range []string{
		"In this loop workflow, the `done` tool is the explicitly required completion signal",
		"Pass the complete final Markdown completion report in the `done` tool's required `report` argument, following the report structure in its tool description",
		"Do not call the `done` tool while required work or a user decision remains",
		"Required verification is completed, or explicitly reported as not run",
		"If verification cannot be run, state why in the final report.",
		"continue working instead of calling `done`",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("loop completion requirements should include %q, got %q", want, joined)
		}
	}
	if strings.Contains(joined, "no unresolved user decision, error, or verification remains") {
		t.Fatalf("loop completion must allow explicitly unrun verification: %q", joined)
	}
	// The report structure itself lives in the Done tool description (single
	// source referenced by the loop requirement line above).
	desc := tools.DoneTool{}.Description()
	for _, want := range []string{
		"**Completion status**: one line summary",
		"**What changed**: files modified, created, deleted or key actions taken",
		"**Verification**: tests run and their results",
		"**Remaining issues**: any limitations, unverified areas, or known issues",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Done description should include %q, got %q", want, desc)
		}
	}
}

func TestEvaluateToolPermission_TreatsQuestionAskAsAllow(t *testing.T) {
	ruleset := permission.Ruleset{{Permission: tools.NameQuestion, Pattern: "*", Action: permission.ActionAsk}}
	decision := evaluateToolPermissionInDir(ruleset, tools.NameQuestion, []byte(`{"questions":[{"header":"Next","question":"What next?","options":[]}]}`), permission.PathScope{})
	if decision.Action != permission.ActionAllow {
		t.Fatalf("Question permission action = %q, want allow", decision.Action)
	}
	if len(decision.NeedsApprovalPaths) != 0 {
		t.Fatalf("Question ask should not require approval paths, got %v", decision.NeedsApprovalPaths)
	}
}

func TestNormalizeToolPermissionActionLeavesDoneAskAsAsk(t *testing.T) {
	if got := normalizeToolPermissionAction(tools.NameDone, permission.ActionAsk); got != permission.ActionAsk {
		t.Fatalf("normalizeToolPermissionAction(Done, ask) = %q, want ask", got)
	}
}

func TestSharedCodingGuidelinesPrompt_RequiresHighQualityClarificationsWithoutQuestionTool(t *testing.T) {
	got := sharedCodingGuidelinesPrompt
	for _, want := range []string{
		"When a clarification or decision is necessary",
		"make it easy for a non-implementer to answer",
		"their tradeoffs/risks, and your recommended default",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedCodingGuidelinesPrompt missing %q in %q", want, got)
		}
	}
}

func TestSharedCodingGuidelinesPrompt_PrefersReasonableAutonomyBeforeAsking(t *testing.T) {
	got := sharedCodingGuidelinesPrompt
	for _, want := range []string{
		"pick the one with the smallest blast radius on existing code and proceed without asking",
		"If multiple interpretations exist but one is clearly the best fit",
		"Ask before implementing only when missing information is genuinely blocking",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedCodingGuidelinesPrompt missing %q in %q", want, got)
		}
	}
}

// Each Guidelines rule has one source: the repeat rule owns when a command,
// search or approach may run again, and the merged ambiguity bullet owns the
// sibling-implementation preference. The dropped restatements must not return.
func TestSharedCodingGuidelinesPrompt_RulesStaySingleSourced(t *testing.T) {
	got := sharedCodingGuidelinesPrompt
	for _, unwanted := range []string{
		"Revisit an exhausted search or failed approach only when",
		"the result cannot change",
		"Only count a run as valid",
		"is not a reason to re-run",
		"full suite at most once",
		"When the spec is ambiguous",
		"acceptance criteria you are only guessing at",
		"note the assumption in the file",
		"Avoid engineering from first principles",
		"import restrictions",
		"task notes",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sharedCodingGuidelinesPrompt repeats or reintroduces %q in %q", unwanted, got)
		}
	}
	if got := strings.Count(got, "only when something relevant changed since the last attempt"); got != 1 {
		t.Fatalf("repeat rule rendered %d times, want 1", got)
	}
	// The exhausted-search guidance lives only in the merged repeat-rule bullet,
	// not also at the end of the exploration bullet.
	if got := strings.Count(got, "When a search or approach is exhausted"); got != 1 {
		t.Fatalf("exhausted-search guidance rendered %d times, want 1", got)
	}
}

func TestSharedCodingGuidelinesPrompt_SeparatesProductLevelAndImplementationLevelAmbiguity(t *testing.T) {
	got := sharedCodingGuidelinesPrompt
	for _, want := range []string{
		"desired product behavior genuinely ambiguous in ways the user would directly perceive",
		"surface the open product decisions to the user before implementing rather than silently picking the simplest interpretation",
		"If the user has explicitly indicated a minimal or specific scope",
		"treat that as the resolved product decision and proceed without re-asking",
		"When the request admits several implementation paths with no externally visible behavior difference",
		"pick the one with the smallest blast radius on existing code",
		"proceed with it and state the assumption briefly; to find it, prefer the closest existing sibling implementation and the documented contract",
		"do not add special cases for requirements you are only guessing at",
		"If a blocker of this kind appears mid-execution, raise it then rather than continuing on a guess or pretending the task is complete",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedCodingGuidelinesPrompt missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{
		"if assumptions are uncertain, ask before implementing",
		"if anything is unclear, stop and ask",
		"do only what was asked",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sharedCodingGuidelinesPrompt unexpectedly reintroduced anti-pattern phrasing %q in %q", unwanted, got)
		}
	}
}

func TestSharedCodingGuidelinesPrompt_RequiresEvidenceDiscriminationAndAmbiguityConvergence(t *testing.T) {
	got := sharedCodingGuidelinesPrompt
	for _, want := range []string{
		// Evidence discrimination.
		"When you identify a concrete path that may violate a requirement",
		"check it with the smallest reproduction",
		"failure, interruption, ordering, and resource constraints",
		"Confirm a reachable path and the surrounding guards before calling something a defect",
		"state the goal and constraints before recommending",
		"separate what was observed from what was inferred before naming a cause",
		"distinguish requirements from assumptions",
		"Before claiming completion, compare the requested behavior",
		"For analysis, review, or planning tasks",
		"run dynamic checks only when requested or necessary",
		"When a self-contained fact (library semantics, dtype/null behavior, version differences, API availability) can be settled by a one-off script in seconds, run it instead of deriving it",
		"this does not cover multi-file interactions, concurrency behavior, or state that requires a running service",
		// Ambiguity convergence.
		"distinguish requirements from assumptions",
		"clarify when blocked",
		"limit claims to what you verified",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedCodingGuidelinesPrompt missing %q in %q", want, got)
		}
	}
	// The rules are shared with SubAgents, not main-only guidance.
	if !strings.Contains(subAgentCodingGuidelinesPrompt, "clarify when blocked") {
		t.Fatalf("subAgentCodingGuidelinesPrompt missing shared ambiguity-convergence rule: %q", subAgentCodingGuidelinesPrompt)
	}
	for _, unwanted := range []string{
		"if assumptions are uncertain, ask before implementing",
		"if anything is unclear, stop and ask",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sharedCodingGuidelinesPrompt unexpectedly contains %q in %q", unwanted, got)
		}
	}
}

func TestSharedAgentValuesPrompt_AllowsNecessaryLowRiskAdjacentWork(t *testing.T) {
	got := sharedAgentValuesPrompt
	for _, want := range []string{
		"Verify > Assume — verify claims proportionally to the task and risk",
		"Complete the requested outcome with the smallest safe change set",
		"targeted regression tests",
		"required doc updates",
		"Match the user's current language for all user-facing text",
		"completion reports, confirmation content, tool arguments intended for display",
		"Small cross-file extractions made to reuse an existing abstraction count as minimal",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedAgentValuesPrompt missing %q in %q", want, got)
		}
	}
}

func TestUserConfirmationPromptBlock_RequiresContextTradeoffsAndRecommendation(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(testQuestionTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
question: allow
`)}
	a.rebuildRuleset()
	got := a.userConfirmationPromptBlock()
	for _, want := range []string{
		"Default to making ordinary implementation decisions yourself",
		"the Guidelines section defines when asking the user is justified and what information a question must carry",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("userConfirmationPromptBlock missing %q in %q", want, got)
		}
	}
	// The referenced information standard must actually exist in Guidelines
	// (single source for both Main and Sub agents).
	for _, want := range []string{
		"make it easy for a non-implementer to answer",
		"the main options, their tradeoffs/risks, and your recommended default",
	} {
		if !strings.Contains(sharedCodingGuidelinesPrompt, want) {
			t.Fatalf("sharedCodingGuidelinesPrompt missing question information standard %q", want)
		}
	}
}

// How a response ends lives in Response Closure alone; User Communication keeps
// the in-flight orientation guidance.
func TestMainAgentCommunicationPrompt_PrefersAutonomyForLowRiskAdjacentWork(t *testing.T) {
	closure := mainAgentResponseClosurePromptText(false)
	for _, want := range []string{
		"Do not end responses with open-ended optional offers for routine in-scope next steps",
		"This applies to equivalent wording in any language",
		"if the next step is clearly necessary, low-risk, and within scope, do it yourself instead of offering it or asking the user to decide",
	} {
		if !strings.Contains(closure, want) {
			t.Fatalf("response closure missing %q in %q", want, closure)
		}
		if strings.Contains(mainAgentCommunicationPrompt, want) {
			t.Fatalf("mainAgentCommunicationPrompt restates closure rule %q", want)
		}
	}
	if !strings.Contains(mainAgentCommunicationPrompt, "keep the user oriented about the current direction") {
		t.Fatalf("mainAgentCommunicationPrompt lost orientation guidance: %q", mainAgentCommunicationPrompt)
	}
}

func TestMainAgentResponseClosurePrompt_RequiresContinueUnlessBlocked(t *testing.T) {
	got := mainAgentResponseClosurePromptText(true)
	for _, want := range []string{
		"Within a normal turn, continue until the current in-scope work package is finished",
		"A regular assistant response is not the end of the task when in-scope, low-risk work still remains; continue instead of stopping with a partial summary or optional offer",
		"ask exactly the necessary high-context question instead of pretending the task is complete",
		// done is mounted only while a loop runs, so the visible branch is the
		// in-loop wording: route the report through the tool rather than
		// arguing about whether the tool applies at all.
		"Return that completion report through the `done` tool's `report` argument",
		"following the active loop workflow's completion contract",
		"After reporting completion, stop there; do not append routine in-scope follow-up work as an optional invitation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentResponseClosurePromptText(true) missing %q in %q", want, got)
		}
	}

	withoutDone := mainAgentResponseClosurePromptText(false)
	if strings.Contains(withoutDone, "Done") {
		t.Fatalf("mainAgentResponseClosurePromptText(false) must not reference the hidden Done tool: %q", withoutDone)
	}
	if !strings.Contains(withoutDone, "Return that completion report directly in the final assistant response") {
		t.Fatalf("mainAgentResponseClosurePromptText(false) missing completion report guidance: %q", withoutDone)
	}
}

func TestLoopCompletionRequirementLinesIncludeDoneToolContract(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	joined := strings.Join(a.loopCompletionRequirementLines(), "\n")
	// The report structure lives in the Done tool description; the loop line
	// must reference it and require the report argument.
	if !strings.Contains(joined, "Pass the complete final Markdown completion report in the `done` tool's required `report` argument, following the report structure in its tool description") {
		t.Fatalf("loop completion requirements should reference the Done report structure, got %q", joined)
	}
	if !strings.Contains(joined, "In this loop workflow, the `done` tool is the explicitly required completion signal") {
		t.Fatalf("loop completion requirements should designate Done as the required completion signal, got %q", joined)
	}
	// The general completion-report wording lives in Response Closure; the loop
	// contract does not restate it as a separate final-response section.
	if strings.Contains(joined, "Clearly state that the requested task is complete") {
		t.Fatalf("loop completion requirements should not restate Response Closure, got %q", joined)
	}
}

// ---------------------------------------------------------------------------
// gitStatus injection
// ---------------------------------------------------------------------------

func TestLoopCompletionRequirementLinesUsePermissionSpecificConfirmationGuidance(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	lines := a.loopCompletionRequirementLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Do not call the `done` tool while required work or a user decision remains") {
		t.Fatalf("loop completion requirements should describe stricter Done usage, got %q", joined)
	}
	if strings.Contains(joined, "Question tool") {
		t.Fatalf("loop completion requirements without Question should not require Question tool, got %q", joined)
	}
	if strings.Contains(joined, "completion follow-up `question` call") {
		t.Fatalf("loop completion requirements without Question should not mention completion follow-up Question, got %q", joined)
	}

	a.tools.Register(testQuestionTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
question: allow
`)}
	a.rebuildRuleset()
	joined = strings.Join(a.loopCompletionRequirementLines(), "\n")
	if strings.Contains(joined, "call the `question` tool") {
		t.Fatalf("loop completion requirements should not generally require Question during loop completion, got %q", joined)
	}
	if !strings.Contains(joined, "Do not call the `done` tool while required work or a user decision remains") {
		t.Fatalf("loop completion requirements should preserve Done gating, got %q", joined)
	}
	if strings.Contains(joined, "unless the current task is already complete and you are making the final completion follow-up `question` call") {
		t.Fatalf("loop completion requirements should not mention completion follow-up exception, got %q", joined)
	}
}
