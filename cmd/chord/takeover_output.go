package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

func validateTakeoverCLIArguments(action, sessionID, transferID, targetKind, targetInstance string) error {
	if !takeover.ValidID(sessionID, 128) || !takeover.ValidID(transferID, 128) {
		return &takeover.Error{Code: takeover.CodeInvalidRequest, Message: "bounded session and transfer identities are required"}
	}
	if (action == takeover.ActionStart || action == takeover.ActionPreflight) && ((targetKind != takeover.OwnerTUI && targetKind != takeover.OwnerGateway) || !takeover.ValidID(targetInstance, 128)) {
		return &takeover.Error{Code: takeover.CodeInvalidRequest, Message: "target kind and bounded target identity are required"}
	}
	return nil
}

// Invalid identities are omitted instead of truncating them into another ID.
func boundedTakeoverCLIIdentity(id string) string {
	if takeover.ValidID(id, 128) {
		return id
	}
	return ""
}

func takeoverCLIErrorCode(err error) string {
	if typed, ok := errors.AsType[*takeover.Error](err); ok {
		return knownTakeoverErrorCode(typed.Code)
	}
	if typed, ok := errors.AsType[*recovery.SessionPauseError](err); ok {
		return knownTakeoverErrorCode(typed.Code)
	}
	code, _, _ := strings.Cut(err.Error(), ":")
	return knownTakeoverErrorCode(code)
}

func knownTakeoverErrorCode(code string) string {
	switch code {
	case takeover.CodeInvalidRequest, takeover.CodeUnsupportedPlatform, takeover.CodeWrongOwnerKind, takeover.CodeWrongWorkspace, takeover.CodeStaleSource, takeover.CodeTransferBusy, takeover.CodeTargetNotReady, takeover.CodeInteractionBlocked, takeover.CodePersistenceFailed, takeover.CodeLockBusy, takeover.CodeRestoreFailed, takeover.CodeStatusUnknown:
		return code
	default:
		return takeover.CodeInvalidRequest
	}
}

// Encode before writing so an oversized status cannot partially contaminate
// the machine-readable stdout stream.
func writeTakeoverCLIStatus(out io.Writer, status recovery.SessionPauseStatus) error {
	return writeTakeoverCLIPublicStatus(out, takeover.PublicStatus(status))
}

func writeTakeoverCLIPublicStatus(out io.Writer, status takeover.Status) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	if len(data)+1 > takeover.MaxFrameBytes {
		return &takeover.Error{Code: takeover.CodeInvalidRequest, Message: "transfer status exceeds JSON output limit"}
	}
	data = append(data, '\n')
	n, err := out.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
