package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	mailboxArtifactPayloadThreshold = 320
	replyArtifactPayloadThreshold   = 320
)

func artifactTypeForMailboxKind(kind SubAgentMailboxKind) string {
	switch kind {
	case SubAgentMailboxKindCompleted:
		return "verification_report"
	case SubAgentMailboxKindProgress:
		return "research_report"
	default:
		return "execution_spec"
	}
}

func sanitizeArtifactType(artifactType string) string {
	artifactType = strings.TrimSpace(strings.ToLower(artifactType))
	if artifactType == "" {
		return "handoff_note"
	}
	var b strings.Builder
	for _, r := range artifactType {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "handoff_note"
	}
	return out
}

func sessionRelativePath(sessionDir, absPath string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	absPath = strings.TrimSpace(absPath)
	if sessionDir == "" || absPath == "" {
		return ""
	}
	rel, err := filepath.Rel(sessionDir, absPath)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

func persistSubAgentArtifact(sessionDir, agentID, baseID, artifactType, title, body string) (artifactID, artifactRelPath string, err error) {
	sessionDir = strings.TrimSpace(sessionDir)
	agentID = strings.TrimSpace(agentID)
	baseID = strings.TrimSpace(baseID)
	artifactType = sanitizeArtifactType(artifactType)
	body = strings.TrimSpace(body)
	if sessionDir == "" || agentID == "" || baseID == "" || body == "" {
		return "", "", nil
	}
	dir := filepath.Join(sessionDir, "artifacts", "subagents", agentID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	artifactID = fmt.Sprintf("%s-%s", baseID, artifactType)
	path := filepath.Join(dir, artifactID+".md")
	var b strings.Builder
	if strings.TrimSpace(title) != "" {
		b.WriteString("# ")
		b.WriteString(strings.TrimSpace(title))
		b.WriteString("\n\n")
	}
	b.WriteString(body)
	b.WriteString("\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", "", err
	}
	return artifactID, sessionRelativePath(sessionDir, path), nil
}
