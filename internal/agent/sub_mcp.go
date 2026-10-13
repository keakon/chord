package agent

import "context"

func (s *SubAgent) prepareMCPRequest(ctx context.Context) error {
	if s.mcpPreparation == nil {
		return nil
	}
	snapshot, err := s.mcpPreparation(ctx, s.currentRuleset())
	if err != nil {
		return err
	}
	s.llmMu.RLock()
	unchanged := s.mcpSnapshotVersion == snapshot.Version
	s.llmMu.RUnlock()
	if unchanged {
		return nil
	}
	s.tools.ReplaceMCPTools(snapshot.Tools)
	definitions := llmToolDefinitionsFromVisibleTools(s.filteredVisibleTools())
	s.llmMu.Lock()
	s.frozenToolDefs = definitions
	s.mcpSnapshotVersion = snapshot.Version
	s.llmMu.Unlock()
	return nil
}
