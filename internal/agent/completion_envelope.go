package agent

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

func normalizeStringList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneAgentResult(result *AgentResult) *AgentResult {
	if result == nil {
		return nil
	}
	out := *result
	out.Summary = strings.TrimSpace(out.Summary)
	out.Envelope = normalizeCompletionEnvelope(out.Envelope)
	return &out
}
func normalizeCompletionEnvelope(env *CompletionEnvelope) *CompletionEnvelope {
	if env == nil {
		return nil
	}
	out := *env
	out.Summary = strings.TrimSpace(out.Summary)
	out.FilesChanged = normalizeStringList(out.FilesChanged)
	out.ReportedFilesChanged = normalizeStringList(out.ReportedFilesChanged)
	out.ActualFilesChanged = normalizeStringList(out.ActualFilesChanged)
	out.VerificationRun = normalizeStringList(out.VerificationRun)
	if len(out.VerificationRecords) > 0 {
		records := make([]VerificationRecord, 0, len(out.VerificationRecords))
		for _, record := range out.VerificationRecords {
			record.ToolCallID = strings.TrimSpace(record.ToolCallID)
			record.Command = strings.TrimSpace(record.Command)
			record.Status = strings.TrimSpace(record.Status)
			record.Summary = strings.TrimSpace(record.Summary)
			if record.Command != "" {
				records = append(records, record)
			}
		}
		out.VerificationRecords = records
	}
	out.RemainingLimitations = normalizeStringList(out.RemainingLimitations)
	out.KnownRisks = normalizeStringList(out.KnownRisks)
	out.FollowUpRecommended = normalizeStringList(out.FollowUpRecommended)
	out.Artifacts = tools.NormalizeArtifactRefs(out.Artifacts)
	out.ResultType = strings.TrimSpace(out.ResultType)
	out.Result = append(json.RawMessage(nil), bytes.TrimSpace(out.Result)...)
	if out.ResultRef != nil {
		ref := *out.ResultRef
		out.ResultRef = &ref
	}
	if out.Summary == "" && len(out.FilesChanged) == 0 && len(out.ReportedFilesChanged) == 0 && len(out.ActualFilesChanged) == 0 && !out.FileAttributionIncomplete && len(out.VerificationRun) == 0 && len(out.VerificationRecords) == 0 && len(out.RemainingLimitations) == 0 && len(out.KnownRisks) == 0 && len(out.FollowUpRecommended) == 0 && len(out.Artifacts) == 0 && out.ResultType == "" && len(out.Result) == 0 && out.ResultRef == nil {
		return nil
	}
	return &out
}

func mergeArtifactRefs(groups ...[]tools.ArtifactRef) []tools.ArtifactRef {
	var all []tools.ArtifactRef
	for _, group := range groups {
		all = append(all, group...)
	}
	return tools.NormalizeArtifactRefs(all)
}
