package session

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// ---------------------------------------------------------------------------
// Supported versions
// ---------------------------------------------------------------------------

// supportedVersions lists the export format versions that this build can import.
var supportedVersions = map[string]bool{
	"1": true,
}

// ---------------------------------------------------------------------------
// Import functions
// ---------------------------------------------------------------------------

// ImportFromFile reads a session export JSON file and returns the parsed
// ExportedSession. The file is validated after parsing (version check,
// required fields).
func ImportFromFile(path string) (*ExportedSession, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session file %s: %w", path, err)
	}

	var session ExportedSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("parse session file %s: %w", path, err)
	}

	if err := ValidateSession(&session); err != nil {
		return nil, fmt.Errorf("validate session from %s: %w", path, err)
	}

	return &session, nil
}

// ImportFromBytes parses a session export from raw JSON bytes. Useful for
// testing or reading from non-file sources. The session is validated after
// parsing.
func ImportFromBytes(data []byte) (*ExportedSession, error) {
	var session ExportedSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("parse session data: %w", err)
	}

	if err := ValidateSession(&session); err != nil {
		return nil, fmt.Errorf("validate session: %w", err)
	}

	return &session, nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// ValidateSession checks that a parsed ExportedSession has all required fields
// and a compatible version. Returns nil if valid.
//
// Validation rules:
//   - Version must be non-empty and in the supported set
//   - CreatedAt must not be zero
//   - Messages slice must be present (may be empty)
//   - Each message must have a non-empty Role
func ValidateSession(session *ExportedSession) error {
	if session == nil {
		return fmt.Errorf("session is nil")
	}

	// Version check.
	if session.Version == "" {
		return fmt.Errorf("missing version field")
	}
	if !supportedVersions[session.Version] {
		return fmt.Errorf("unsupported session version %q (supported: %v)",
			session.Version, supportedVersionKeys())
	}

	// CreatedAt check.
	if session.CreatedAt.IsZero() {
		return fmt.Errorf("missing created_at timestamp")
	}

	// Messages check — the slice itself must be present (even if empty).
	if session.Messages == nil {
		return fmt.Errorf("missing messages field")
	}

	// Validate individual messages.
	for i, msg := range session.Messages {
		if msg.Role == "" {
			return fmt.Errorf("message %d: missing role", i)
		}
		// Validate role is one of the known values.
		switch msg.Role {
		case "user", "assistant", "tool", "system":
			// ok
		default:
			return fmt.Errorf("message %d: unknown role %q", i, msg.Role)
		}
	}

	return nil
}

// supportedVersionKeys returns the supported version strings as a sorted slice
// for use in error messages.
func supportedVersionKeys() []string {
	keys := make([]string, 0, len(supportedVersions))
	for k := range supportedVersions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
