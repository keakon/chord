package imagecache

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPinnedSourceKeyDoesNotInspectFile(t *testing.T) {
	stat := imageCacheStat
	defer func() { imageCacheStat = stat }()
	imageCacheStat = func(string) (os.FileInfo, error) { t.Fatal("pinned key inspected file"); return nil, nil }
	source := Source{Path: "/sample/image.png", MIME: "image/png", Key: "accepted-version"}
	if key, err := Key(source); err != nil || key != source.Key {
		t.Fatalf("key=%q err=%v", key, err)
	}
	if _, err := New().Lookup(source, false); err != nil {
		t.Fatal(err)
	}
}

func TestFreshSourceKeyInspectsReplacedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.png")
	if err := os.WriteFile(path, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	source := Source{Path: path, MIME: "image/png", Key: "accepted-version"}
	before, err := FreshKey(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := FreshKey(source)
	if err != nil || before == after {
		t.Fatalf("replacement kept key: %q %v", after, err)
	}
}

func TestReadyConfigDoesNotWaitForTransportLock(t *testing.T) {
	source := Source{Data: makeTestPNG(t), MIME: "image/png"}
	entry, err := New().Lookup(source, false)
	if err != nil {
		t.Fatal(err)
	}
	expected, _, err := entry.Config(source)
	if err != nil {
		t.Fatal(err)
	}
	entry.mu.Lock()
	type result struct {
		config image.Config
		err    error
	}
	done := make(chan result, 1)
	go func() { cfg, _, err := entry.Config(source); done <- result{cfg, err} }()
	select {
	case got := <-done:
		entry.mu.Unlock()
		if got.err != nil || got.config.Width != expected.Width || got.config.Height != expected.Height {
			t.Fatalf("config=%+v err=%v", got.config, got.err)
		}
	case <-time.After(5 * time.Second):
		entry.mu.Unlock()
		<-done
		t.Fatal("ready config waited for transport lock")
	}
}
