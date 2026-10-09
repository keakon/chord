package agent

import (
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func restoreNativeTranscript(rm *recovery.RecoveryManager, sessionDir, taskID, instanceID string, history []message.Message) ([]message.Message, error) {
	if rm == nil {
		return history, nil
	}
	journal := recovery.NativeRequestJournal{SessionDir: sessionDir, AgentID: taskID}
	return journal.Restore(history, func(msg message.Message) error {
		return rm.PersistMessageDurable(instanceID, msg)
	})
}

func restoreNativeSessionReceipts(loaded *loadedSessionState, rm *recovery.RecoveryManager) error {
	var err error
	loaded.Messages, err = restoreNativeTranscript(rm, loaded.SessionPath, identity.MainAgentID, identity.MainAgentID, loaded.Messages)
	if err != nil {
		return err
	}
	for i := range loaded.SubAgentStates {
		state := &loaded.SubAgentStates[i]
		taskID := state.TaskID
		if taskID == "" {
			taskID = state.InstanceID
		}
		state.Messages, err = restoreNativeTranscript(rm, loaded.SessionPath, taskID, state.InstanceID, state.Messages)
		if err != nil {
			return err
		}
	}
	return nil
}
