package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/keakon/chord/internal/atomicfile"
)

func storeTestTarget() Target {
	t := Target{Ref: ServerRef{WorkspaceID: "workspace", Owner: MainOwner, ServerName: "sample"}, Config: ServerConfig{URL: "https://example.invalid/mcp"}}
	t.TargetID = identityHash(struct{ Workspace, Owner, Endpoint string }{t.Ref.WorkspaceID, t.Ref.Owner, t.Config.URL})
	t.ProfileID = identityHash(profileForTarget(t))
	return t
}

func storeEnsureTestSlot(ctx context.Context, s *CredentialStore, target Target) error {
	return s.UpdateLocked(ctx, target, func(tx *CredentialTransaction) error {
		tx.File.Slots[target.ProfileID] = &CredentialSlot{Profile: profileForTarget(target), AuthGeneration: 1, Status: CredentialEmpty}
		_, err := tx.Commit()
		return err
	})
}

func TestCredentialStoreInspectDoesNotCreate(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	s := NewCredentialStore(home)
	file, err := s.InspectFile(t.Context(), storeTestTarget())
	if err != nil || file != nil {
		t.Fatalf("inspect: %v %v", file, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("inspect created home: %v", err)
	}
}

func TestCredentialStoreConfirmationAndCache(t *testing.T) {
	s := NewCredentialStore(t.TempDir())
	target := storeTestTarget()
	if err := storeEnsureTestSlot(t.Context(), s, target); err != nil {
		t.Fatal(err)
	}
	other := NewCredentialStore(s.home)
	writes := 0
	other.replace = func(path string, data []byte, mode os.FileMode) (atomicfile.ReplaceOutcome, error) {
		writes++
		return atomicfile.ReplaceDurable(path, data, mode)
	}
	path, _ := s.path(target)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := other.ReadForUse(t.Context(), target); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 1 {
		t.Fatalf("confirmation writes: %d", writes)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("confirmation changed bytes")
	}
	if err := s.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		tx.File.Slots[target.ProfileID].AuthGeneration++
		_, err := tx.Commit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ReadForUse(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("changed version not confirmed: %d", writes)
	}
}

func TestCredentialStoreUnknownReplacementRecovery(t *testing.T) {
	s := NewCredentialStore(t.TempDir())
	target := storeTestTarget()
	if err := storeEnsureTestSlot(t.Context(), s, target); err != nil {
		t.Fatal(err)
	}
	s.replace = func(path string, data []byte, mode os.FileMode) (atomicfile.ReplaceOutcome, error) {
		if _, err := atomicfile.ReplaceDurable(path, data, mode); err != nil {
			return atomicfile.NotReplaced, err
		}
		return atomicfile.ReplacementUnknown, errors.New("injected confirmation failure")
	}
	err := s.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		tx.File.Slots[target.ProfileID].AuthGeneration = 2
		_, err := tx.Commit()
		return err
	})
	se, ok := errors.AsType[*CredentialStoreError](err)
	if !ok || se.Outcome != atomicfile.ReplacementUnknown {
		t.Fatalf("outcome: %v", err)
	}
	s.replace = atomicfile.ReplaceDurable
	file, err := s.ReadForUse(t.Context(), target)
	if err != nil || file.Slots[target.ProfileID].AuthGeneration != 2 {
		t.Fatalf("recovery: %v", err)
	}
}

func TestCredentialStorePreservesOtherProfiles(t *testing.T) {
	s := NewCredentialStore(t.TempDir())
	target := storeTestTarget()
	if err := storeEnsureTestSlot(t.Context(), s, target); err != nil {
		t.Fatal(err)
	}
	other := target.Snapshot()
	p := profileForTarget(other)
	p.IssuerHint = "https://issuer.example.invalid/"
	other.ProfileID = identityHash(p)
	if err := s.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		tx.File.Slots[other.ProfileID] = &CredentialSlot{Profile: p, AuthGeneration: 7, Status: CredentialLoggedOut}
		_, err := tx.Commit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		tx.File.Slots[target.ProfileID].AuthGeneration++
		_, err := tx.Commit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	file, err := s.InspectFile(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Slots) != 2 || file.Slots[other.ProfileID].AuthGeneration != 7 {
		t.Fatal("unrelated profile changed")
	}
}

func TestCredentialStoreFailedConfirmationDoesNotExposeSecrets(t *testing.T) {
	s := NewCredentialStore(t.TempDir())
	target := storeTestTarget()
	if err := storeEnsureTestSlot(t.Context(), s, target); err != nil {
		t.Fatal(err)
	}
	other := NewCredentialStore(s.home)
	other.replace = func(string, []byte, os.FileMode) (atomicfile.ReplaceOutcome, error) {
		return atomicfile.NotReplaced, errors.New("injected persistence failure")
	}
	called := false
	err := other.UpdateLocked(t.Context(), target, func(*CredentialTransaction) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("failed confirmation reached credential user")
	}
}

func TestCredentialStoreRejectsCorruptionWithoutWrite(t *testing.T) {
	for _, kind := range []string{"duplicate", "unknown", "schema", "binding", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			s := NewCredentialStore(t.TempDir())
			target := storeTestTarget()
			if err := storeEnsureTestSlot(t.Context(), s, target); err != nil {
				t.Fatal(err)
			}
			path, _ := s.path(target)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var file map[string]any
			if err := json.Unmarshal(raw, &file); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "duplicate":
				raw = append([]byte(`{"schema_version":1,`), raw[1:]...)
			case "unknown":
				file["unexpected"] = true
				raw, _ = json.Marshal(file)
			case "schema":
				file["schema_version"] = 2
				raw, _ = json.Marshal(file)
			case "binding":
				file["target"].(map[string]any)["owner"] = "other"
				raw, _ = json.Marshal(file)
			case "oversized":
				raw = make([]byte, credentialFileLimit+1)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReadForUse(t.Context(), target); err == nil {
				t.Fatal("accepted corrupt file")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("corrupt file overwritten")
			}
		})
	}
}

func TestCredentialStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	s := NewCredentialStore(t.TempDir())
	target := storeTestTarget()
	if err := storeEnsureTestSlot(t.Context(), s, target); err != nil {
		t.Fatal(err)
	}
	path, _ := s.path(target)
	for _, p := range []string{path, path + ".lock", filepath.Dir(path)} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, info.Mode().Perm()|0o040); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadForUse(t.Context(), target); err == nil {
			t.Fatalf("accepted unsafe permissions: %s", p)
		}
		if err := os.Chmod(p, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCredentialStoreProcessTransactions(t *testing.T) {
	if home := os.Getenv("CHORD_STORE_CHILD_HOME"); home != "" {
		s := NewCredentialStore(home)
		target := storeTestTarget()
		for range 8 {
			if err := s.UpdateLocked(context.Background(), target, func(tx *CredentialTransaction) error {
				tx.File.Slots[target.ProfileID].AuthGeneration++
				_, err := tx.Commit()
				return err
			}); err != nil {
				os.Exit(4)
			}
		}
		os.Exit(0)
	}
	s := NewCredentialStore(t.TempDir())
	target := storeTestTarget()
	if err := storeEnsureTestSlot(t.Context(), s, target); err != nil {
		t.Fatal(err)
	}
	children := make([]*exec.Cmd, 3)
	for i := range children {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCredentialStoreProcessTransactions$")
		cmd.Env = append(os.Environ(), "CHORD_STORE_CHILD_HOME="+s.home)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children[i] = cmd
	}
	for _, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	file, err := s.ReadForUse(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Slots[target.ProfileID].AuthGeneration; got != 25 {
		t.Fatalf("lost transaction: %d", got)
	}
}
