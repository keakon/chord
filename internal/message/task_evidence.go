package message

const MaxTaskEvidencePaths = 128

// TaskEvidence carries runtime observations removed from a worker's transcript.
// It belongs to a durable task, not to a runtime instance or a model's summary.
type TaskEvidence struct {
	TaskID                    string   `json:"task_id"`
	ChangedPaths              []string `json:"changed_paths,omitempty"`
	FileAttributionIncomplete bool     `json:"file_attribution_incomplete,omitempty"`
}

func (e *TaskEvidence) Clone() *TaskEvidence {
	if e == nil {
		return nil
	}
	out := *e
	out.ChangedPaths = append([]string(nil), e.ChangedPaths...)
	return &out
}
