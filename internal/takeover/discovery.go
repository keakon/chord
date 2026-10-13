package takeover

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/recovery"
)

func discoveryDir(stateDir string) string { return filepath.Join(stateDir, "takeover") }
func recordPath(stateDir, runtimeID string) string {
	return filepath.Join(discoveryDir(stateDir), runtimeID+".json")
}
func keyPath(stateDir, runtimeID string) string {
	return filepath.Join(discoveryDir(stateDir), runtimeID+".key")
}

func CanonicalWorkspace(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", protocolError(CodeWrongWorkspace, "workspace is not a directory")
	}
	return filepath.Clean(resolved), nil
}

// ValidID validates a bounded runtime, session or transfer protocol identity.
func ValidID(s string, maxLen int) bool {
	if len(s) == 0 || len(s) > maxLen {
		return false
	}
	for _, r := range s {
		if r != '-' && r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
func normalizeRecord(stateDir string, record Record) (Record, error) {
	if !Supported() {
		return Record{}, protocolError(CodeUnsupportedPlatform, "session takeover requires macOS or Linux")
	}
	if !filepath.IsAbs(stateDir) || !ValidID(record.RuntimeID, 64) || !ValidID(record.SessionID, 128) {
		return Record{}, protocolError(CodeInvalidRequest, "source runtime, session and absolute state directory are required")
	}
	if record.Protocol != ProtocolVersion {
		return Record{}, protocolError(CodeInvalidRequest, "takeover protocol version is unsupported")
	}
	if record.OwnerKind != OwnerTUI && record.OwnerKind != OwnerGateway {
		return Record{}, protocolError(CodeWrongOwnerKind, "source owner kind is unsupported")
	}
	workspace, err := CanonicalWorkspace(record.Workspace)
	if err != nil {
		return Record{}, fmt.Errorf("canonical workspace: %w", err)
	}
	record.Workspace = workspace
	socket := filepath.Join(discoveryDir(stateDir), record.RuntimeID+".sock")
	if len(socket) > MaxSocketPathBytes {
		return Record{}, protocolError(CodeInvalidRequest, "takeover socket path exceeds 100 bytes")
	}
	if record.SocketPath != "" && record.SocketPath != socket {
		return Record{}, protocolError(CodeInvalidRequest, "socket path does not match source runtime")
	}
	record.SocketPath = socket
	return record, nil
}
func validatePrivate(root, path string, mode os.FileMode) (os.FileInfo, error) {
	info, err := privatefs.Stat(root, path)
	if err != nil {
		return nil, err
	}
	if !ownedByCurrentUser(info) || info.Mode().Perm() != mode {
		return nil, protocolError(CodeInvalidRequest, "takeover discovery path has invalid owner or permissions")
	}
	return info, nil
}
func readPrivate(stateDir, path string) ([]byte, error) {
	info, err := validatePrivate(stateDir, path, privatefs.FileMode)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, protocolError(CodeInvalidRequest, "discovery entry is not a regular file")
	}
	f, err := privatefs.OpenFile(stateDir, path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFrameBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFrameBytes {
		return nil, protocolError(CodeInvalidRequest, "discovery entry exceeds frame limit")
	}
	return data, nil
}
func loadRecord(stateDir, path string) (Record, error) {
	data, err := readPrivate(stateDir, path)
	if err != nil {
		return Record{}, err
	}
	var record Record
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, protocolError(CodeInvalidRequest, "discovery record is invalid")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Record{}, protocolError(CodeInvalidRequest, "discovery record must contain one JSON value")
	}
	normalized, err := normalizeRecord(stateDir, record)
	if err != nil {
		return record, err
	}
	if normalized != record {
		return Record{}, protocolError(CodeInvalidRequest, "discovery identity is not canonical")
	}
	if filepath.Base(path) != record.RuntimeID+".json" {
		return Record{}, protocolError(CodeInvalidRequest, "discovery filename does not match runtime")
	}
	return record, nil
}

// Find authenticates live source records, ignoring endpoints left by a crash.
// Endpoint liveness never establishes that a transfer checkpoint was saved.
func Find(stateDir, sessionID string) (Record, error) {
	if !Supported() {
		return Record{}, protocolError(CodeUnsupportedPlatform, "session takeover requires macOS or Linux")
	}
	if !ValidID(sessionID, 128) {
		return Record{}, protocolError(CodeInvalidRequest, "session identity is invalid")
	}
	dir := discoveryDir(stateDir)
	info, err := validatePrivate(stateDir, dir, privatefs.DirMode)
	if os.IsNotExist(err) {
		return Record{}, protocolError(CodeStatusUnknown, "source endpoint was not found")
	}
	if err != nil {
		return Record{}, err
	}
	if !info.IsDir() {
		return Record{}, protocolError(CodeInvalidRequest, "discovery path is not a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Record{}, err
	}
	var matches []Record
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := loadRecord(stateDir, filepath.Join(dir, entry.Name()))
		if record.SessionID != sessionID {
			continue
		}
		if err != nil {
			return Record{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), HandshakeTimeout+WriteTimeout)
		reply, _ := (Client{Record: record, StateDir: stateDir}).exchange(ctx, ActionStatus, recovery.TransferIdentity{TransferID: "discovery"}, 0)
		cancel()
		if reply.Source == record {
			matches = append(matches, record)
		}
	}
	if len(matches) == 0 {
		return Record{}, protocolError(CodeStatusUnknown, "source endpoint was not found")
	}
	if len(matches) > 1 {
		return Record{}, protocolError(CodeStaleSource, "multiple runtime records claim this session")
	}
	return matches[0], nil
}
func readSecret(stateDir, runtimeID string) (string, error) {
	data, err := readPrivate(stateDir, keyPath(stateDir, runtimeID))
	if err != nil {
		return "", err
	}
	if len(data) != 32 {
		return "", protocolError(CodeInvalidRequest, "source key is invalid")
	}
	return hex.EncodeToString(data), nil
}

// writeAtomic returns the published inode even if directory synchronization
// fails, so the listener can clean up a record it already replaced.
func writeAtomic(stateDir, path string, data []byte) (os.FileInfo, error) {
	if info, err := privatefs.Stat(stateDir, path); err == nil {
		if !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode().Perm() != privatefs.FileMode {
			return nil, protocolError(CodeInvalidRequest, "discovery record has invalid type or ownership")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	tmp := path + "." + rand.Text() + ".tmp"
	f, err := privatefs.OpenFile(stateDir, tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL)
	if err != nil {
		return nil, err
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	info, statErr := f.Stat()
	closeErr := errors.Join(statErr, f.Close())
	if writeErr != nil || closeErr != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("write discovery record: %w", errors.Join(writeErr, closeErr))
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	defer root.Close()
	if err := root.Rename(filepath.Base(tmp), filepath.Base(path)); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return info, privatefs.SyncDir(filepath.Dir(path))
}
