package agent

import (
	"fmt"
	"strings"
)

func renderCheckpointClaims(req *modelDrivenCheckpointRequest) string {
	claims := effectiveCheckpointClaims(req)
	if len(claims) == 0 {
		return ""
	}
	var result strings.Builder
	for _, key := range sortedCheckpointClaimKeys(claims) {
		claim := claims[key]
		status := claim.Status
		if override := req.ClaimStatuses[key]; override != "" {
			status = override
		}
		fmt.Fprintf(&result, "- %s", checkpointInlineValue(key))
		if kind := checkpointInlineValue(claim.Kind); kind != "" {
			fmt.Fprintf(&result, " | kind: %s", kind)
		}
		if status = checkpointInlineValue(status); status != "" {
			fmt.Fprintf(&result, " | status: %s", status)
		}
		if len(claim.EvidenceRefs) > 0 {
			fmt.Fprintf(&result, " | evidence: %s", checkpointInlineValue(strings.Join(claim.EvidenceRefs, ", ")))
		}
		result.WriteByte('\n')
	}
	return strings.TrimSpace(result.String())
}
