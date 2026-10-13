package agent

import (
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// skillActivationCandidate pins matching to the tool's execution binding and
// catalog. Completion only revalidates these exact metadata references.
type skillActivationCandidate struct {
	SessionEpoch uint64
	Generation   uint64
	TaskID       string
	CheckoutRoot string
	Refs         []message.SkillActivationRef
}

func (a *MainAgent) skillActivationView() skillActivationView {
	v := skillActivationView{TaskID: identity.MainAgentID, Catalog: a.loadedSkillsSnapshot(), Ruleset: a.effectiveRuleset()}
	if r := a.directoryInstructions.Load(); r != nil {
		v.CheckoutRoot = r.binding.CheckoutRoot
	}
	return v
}
func (s *SubAgent) skillActivationView() skillActivationView {
	catalog, ruleset := s.skillCatalogAndRuleset()
	v := skillActivationView{TaskID: s.taskID, Catalog: catalog, Ruleset: ruleset}
	if r := s.directoryInstructions.Load(); r != nil {
		v.CheckoutRoot = r.binding.CheckoutRoot
	}
	return v
}
func (p toolExecutionPipeline) captureSkillActivation(tc message.ToolCall, observation tools.ReadObservation) *skillActivationCandidate {
	if tc.Name != tools.NameRead || observation.ActivationPath == "" {
		return nil
	}
	refs := matchSkillActivationPaths([]string{observation.ActivationPath}, p.skillView)
	if len(refs) == 0 {
		return nil
	}
	return &skillActivationCandidate{SessionEpoch: p.skillSessionEpoch, Generation: p.toolBaseDirGeneration, TaskID: p.skillView.TaskID, CheckoutRoot: p.skillView.CheckoutRoot, Refs: refs}
}
func skillActivationForResult(history func() []message.Message, currentView func() skillActivationView, epoch, generation uint64, name, callID string, err error, candidate *skillActivationCandidate) *message.SkillActivationFact {
	if candidate == nil || len(candidate.Refs) == 0 || name != tools.NameRead || err != nil || candidate.SessionEpoch != epoch || candidate.Generation != generation {
		return nil
	}
	view := currentView()
	if candidate.TaskID != view.TaskID || candidate.CheckoutRoot != view.CheckoutRoot {
		return nil
	}
	return buildSkillActivationFact(history(), view, callID, candidate.Refs)
}
