package trust

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func writeFileForTrust(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func setFromDir(t *testing.T, root string, files map[string]string) InputSet {
	t.Helper()
	input := Input{Root: root}
	for rel, content := range files {
		writeFileForTrust(t, filepath.Join(root, filepath.FromSlash(rel)), content)
		sum, err := HashFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("hash %s: %v", rel, err)
		}
		input.Files = append(input.Files, FileHash{Path: rel, SHA256: sum})
	}
	return InputSet{input}.Normalize()
}

func TestInputSetFingerprintStableAndSensitive(t *testing.T) {
	dir := t.TempDir()
	set := setFromDir(t, dir, map[string]string{
		".chord/config.yaml":        "mcp: {}\n",
		".chord/agents/review.yaml": "name: review\n",
	})
	reordered := InputSet{{Root: dir, Files: []FileHash{set[0].Files[1], set[0].Files[0]}}}
	if set.Fingerprint() != reordered.Fingerprint() {
		t.Fatal("declaration order changed the fingerprint")
	}
	if set.Empty() {
		t.Fatal("non-empty set reported empty")
	}

	changed := setFromDir(t, dir, map[string]string{
		".chord/config.yaml":        "mcp: {}\n",
		".chord/agents/review.yaml": "name: reviewer\n",
	})
	if changed.Fingerprint() == set.Fingerprint() {
		t.Fatal("content change did not change the fingerprint")
	}

	added := setFromDir(t, dir, map[string]string{
		".chord/config.yaml":        "mcp: {}\n",
		".chord/agents/review.yaml": "name: review\n",
		".chord/commands/deploy.md": "deploy\n",
	})
	if added.Fingerprint() == set.Fingerprint() {
		t.Fatal("added file did not change the fingerprint")
	}

	removed := setFromDir(t, dir, map[string]string{".chord/config.yaml": "mcp: {}\n"})
	if removed.Fingerprint() == set.Fingerprint() {
		t.Fatal("removed file did not change the fingerprint")
	}
}

func TestInputSetFingerprintBindsRoots(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	inA := setFromDir(t, dirA, map[string]string{".chord/skills/commit/SKILL.md": "# commit\n"})
	inB := setFromDir(t, dirB, map[string]string{".chord/skills/commit/SKILL.md": "# commit\n"})
	if inA.Fingerprint() == inB.Fingerprint() {
		t.Fatal("identical content at different roots must produce different fingerprints")
	}
	split := InputSet{
		inA[0],
		{Root: dirB, Files: []FileHash{{Path: ".chord/config.yaml", SHA256: "aa"}}},
	}.Normalize()
	merged := InputSet{
		{Root: dirA, Files: append(append([]FileHash{}, inA[0].Files...), FileHash{Path: ".chord/config.yaml", SHA256: "aa"})},
	}.Normalize()
	if split.Fingerprint() == merged.Fingerprint() {
		t.Fatal("root attribution must affect the fingerprint")
	}
}

func TestInputSetNormalize(t *testing.T) {
	set := InputSet{
		{Root: "/b", Files: []FileHash{{Path: "z", SHA256: "2"}, {Path: "a", SHA256: "1"}}},
		{Root: "/a", Files: nil},
		{Root: "", Files: []FileHash{{Path: "x", SHA256: "3"}}},
	}.Normalize()
	if len(set) != 2 || set[0].Root != "/a" || set[1].Root != "/b" {
		t.Fatalf("controlled empty roots must remain, got %+v", set)
	}
	if set[1].Files[0].Path != "a" || set[1].Files[1].Path != "z" {
		t.Fatalf("files must be sorted by path, got %+v", set[1].Files)
	}
}

func TestInputSetCovers(t *testing.T) {
	authorized := InputSet{
		{Root: "/root", Files: []FileHash{
			{Path: ".chord/skills/a/SKILL.md", SHA256: "aa"},
			{Path: ".chord/skills/a/chord.yaml", SHA256: "bb"},
		}},
	}.Normalize()
	if !authorized.Covers("/root", []FileHash{{Path: ".chord/skills/a/SKILL.md", SHA256: "aa"}}) {
		t.Fatal("subset of authorized files must be covered")
	}
	if !authorized.Covers("/root", nil) {
		t.Fatal("empty file set must be covered")
	}
	if authorized.Covers("/root", []FileHash{{Path: ".chord/skills/a/SKILL.md", SHA256: "cc"}}) {
		t.Fatal("changed hash must not be covered")
	}
	if authorized.Covers("/root", []FileHash{{Path: ".chord/skills/b/SKILL.md", SHA256: "aa"}}) {
		t.Fatal("unknown file must not be covered")
	}
	if authorized.Covers("/other", []FileHash{{Path: ".chord/skills/a/SKILL.md", SHA256: "aa"}}) {
		t.Fatal("unknown root must not be covered")
	}
}

func TestInputSetVerify(t *testing.T) {
	dir := t.TempDir()
	set := setFromDir(t, dir, map[string]string{".chord/config.yaml": "hooks: []\n"})
	if err := set.Verify(); err != nil {
		t.Fatalf("verify on unchanged files: %v", err)
	}
	writeFileForTrust(t, filepath.Join(dir, ".chord/config.yaml"), "hooks: [changed]\n")
	if err := set.Verify(); err == nil {
		t.Fatal("verify must fail when content changed")
	}

	missingDir := t.TempDir()
	ghost := setFromDir(t, missingDir, map[string]string{".chord/config.yaml": "x: 1\n"})
	if err := os.WriteFile(filepath.Join(missingDir, ".chord/config.yaml"), []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ghost.Verify(); err != nil {
		t.Fatalf("verify before removal: %v", err)
	}
	if err := os.Remove(filepath.Join(missingDir, ".chord/config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := ghost.Verify(); err == nil {
		t.Fatal("verify must fail when a file disappeared")
	}
}

func TestStoreAuthorizeLookupRevoke(t *testing.T) {
	storePath := Path(filepath.Join(t.TempDir(), "state"))
	root := "/project/demo"
	inputs := InputSet{{Root: root, Files: []FileHash{{Path: ".chord/config.yaml", SHA256: "aa"}}}}.Normalize()

	if err := Authorize(storePath, root, inputs, time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	store, err := Load(storePath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	record, ok := store.Lookup(root)
	if !ok {
		t.Fatal("authorized record not found")
	}
	if record.Fingerprint != inputs.Fingerprint() {
		t.Fatalf("fingerprint mismatch: got %q want %q", record.Fingerprint, inputs.Fingerprint())
	}
	if record.GrantedAtNs != time.Unix(1700000000, 0).UnixNano() {
		t.Fatalf("granted time mismatch: %d", record.GrantedAtNs)
	}
	if len(record.Inputs) != 1 || len(record.Inputs[0].Files) != 1 {
		t.Fatalf("inputs not persisted: %+v", record.Inputs)
	}

	reAuthorized := InputSet{{Root: root, Files: []FileHash{{Path: ".chord/config.yaml", SHA256: "bb"}}}}.Normalize()
	if err := Authorize(storePath, root, reAuthorized, time.Unix(1700000001, 0)); err != nil {
		t.Fatalf("re-authorize: %v", err)
	}
	store, err = Load(storePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if record, _ = store.Lookup(root); record.Fingerprint != reAuthorized.Fingerprint() {
		t.Fatal("re-authorization must replace the previous record")
	}

	removed, err := Revoke(storePath, root)
	if err != nil || !removed {
		t.Fatalf("revoke: removed=%v err=%v", removed, err)
	}
	removed, err = Revoke(storePath, root)
	if err != nil || removed {
		t.Fatalf("second revoke: removed=%v err=%v", removed, err)
	}
	if store, err = Load(storePath); err != nil {
		t.Fatalf("load after revoke: %v", err)
	}
	if _, ok := store.Lookup(root); ok {
		t.Fatal("record still present after revoke")
	}
}

func TestStoreLoadMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	store, err := Load(Path(dir))
	if err != nil {
		t.Fatalf("missing store must load as empty: %v", err)
	}
	if len(store.Projects) != 0 {
		t.Fatalf("missing store must have no records, got %+v", store.Projects)
	}

	corruptPath := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(corruptPath); err == nil {
		t.Fatal("corrupt store must not load")
	}
}

func TestStoreAuthorizeRejectsEmptyInputs(t *testing.T) {
	storePath := Path(filepath.Join(t.TempDir(), "state"))
	if err := Authorize(storePath, "/p", nil, time.Now()); err == nil {
		t.Fatal("authorize without inputs must fail")
	}
	if err := Authorize(storePath, "", InputSet{{Root: "/p", Files: []FileHash{{Path: "a", SHA256: "1"}}}}.Normalize(), time.Now()); err == nil {
		t.Fatal("authorize without project root must fail")
	}
	if _, err := os.Stat(storePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed authorize must not create the store, stat err=%v", err)
	}
}

func TestStoreConcurrentAuthorizeKeepsBothRecords(t *testing.T) {
	storePath := Path(filepath.Join(t.TempDir(), "state"))
	roots := []string{"/project/a", "/project/b", "/project/c", "/project/d"}
	var wg sync.WaitGroup
	for i, root := range roots {
		wg.Add(1)
		go func(i int, root string) {
			defer wg.Done()
			inputs := InputSet{{Root: root, Files: []FileHash{{Path: ".chord/config.yaml", SHA256: "aa"}}}}.Normalize()
			if err := Authorize(storePath, root, inputs, time.Now()); err != nil {
				t.Errorf("authorize %s: %v", root, err)
			}
		}(i, root)
	}
	wg.Wait()
	store, err := Load(storePath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, root := range roots {
		if _, ok := store.Lookup(root); !ok {
			t.Errorf("record for %s lost under concurrency", root)
		}
	}
}
