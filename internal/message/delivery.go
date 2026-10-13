package message

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
)

const (
	FileLocatorWorkspace = "workspace"
	FileLocatorArtifact  = "artifact"
)

type DeliveredFile struct {
	ID              string         `json:"id"`
	Source          DeliverySource `json:"source"`
	Locator         FileLocator    `json:"locator"`
	DisplayName     string         `json:"display_name"`
	Description     string         `json:"description,omitempty"`
	MIME            string         `json:"mime"`
	SizeBytes       int64          `json:"size_bytes"`
	ModTimeUnixNano int64          `json:"mod_time_unix_nano"`
	SHA256          string         `json:"sha256,omitempty"`
}
type DeliverySource struct {
	SessionID  string `json:"session_id"`
	TaskID     string `json:"task_id"`
	ToolCallID string `json:"tool_call_id"`
	TurnID     string `json:"turn_id"`
	ItemIndex  int    `json:"item_index"`
}
type FileLocator struct {
	Kind      string                `json:"kind"`
	Workspace *WorkspaceFileLocator `json:"workspace,omitempty"`
	Artifact  *ArtifactFileLocator  `json:"artifact,omitempty"`
}
type WorkspaceFileLocator struct {
	Root string `json:"root"`
	Path string `json:"path"`
}
type ArtifactFileLocator struct {
	RelPath string `json:"rel_path"`
}

func DeliveryID(s DeliverySource) string {
	h := sha256.New()
	for _, v := range []string{s.SessionID, s.TaskID, s.ToolCallID, strconv.Itoa(s.ItemIndex)} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(v)))
		h.Write(length[:])
		h.Write([]byte(v))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func CloneDeliveredFiles(files []DeliveredFile) []DeliveredFile {
	if files == nil {
		return nil
	}
	out := append([]DeliveredFile{}, files...)
	for i := range out {
		if p := out[i].Locator.Workspace; p != nil {
			v := *p
			out[i].Locator.Workspace = &v
		}
		if p := out[i].Locator.Artifact; p != nil {
			v := *p
			out[i].Locator.Artifact = &v
		}
	}
	return out
}

// CollectDeliveredFiles carries facts across checkpoints without involving the model.
func CollectDeliveredFiles(messages []Message) []DeliveredFile {
	var out []DeliveredFile
	seen := make(map[string]bool)
	for _, m := range messages {
		for _, list := range [][]DeliveredFile{m.CompactionDeliveredFiles, m.DeliveredFiles} {
			for _, f := range list {
				if f.ID != "" && !seen[f.ID] {
					seen[f.ID] = true
					out = append(out, f)
				}
			}
		}
	}
	return CloneDeliveredFiles(out)
}
