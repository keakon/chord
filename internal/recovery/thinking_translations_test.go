package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestThinkingTranslationsSaveLoadReplacesSlot(t *testing.T) {
	dir := t.TempDir()
	entry := ThinkingTranslationEntry{
		MessageID:    "msgidx:1",
		BlockIndex:   0,
		TargetLang:   "zh-Hans",
		OriginalHash: ThinkingTranslationOriginalHash("thinking"),
		Translated:   "思考",
	}
	if err := SaveThinkingTranslation(dir, entry); err != nil {
		t.Fatalf("SaveThinkingTranslation: %v", err)
	}
	entry.Translated = "思考更新"
	if err := SaveThinkingTranslation(dir, entry); err != nil {
		t.Fatalf("SaveThinkingTranslation replace: %v", err)
	}

	entries, err := LoadThinkingTranslations(dir)
	if err != nil {
		t.Fatalf("LoadThinkingTranslations: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(entries))
	}
	if got := entries[0].Translated; got != "思考更新" {
		t.Fatalf("Translated = %q, want updated translation", got)
	}
	if _, err := os.Stat(filepath.Join(dir, thinkingTranslationsFileName)); err != nil {
		t.Fatalf("thinking translations file not written: %v", err)
	}
}

func TestThinkingTranslationsConcurrentSavesSameSession(t *testing.T) {
	dir := t.TempDir()
	const count = 64

	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := SaveThinkingTranslation(dir, ThinkingTranslationEntry{
				MessageID:    fmt.Sprintf("msgidx:%d", i),
				BlockIndex:   i,
				TargetLang:   "zh-Hans",
				OriginalHash: ThinkingTranslationOriginalHash(fmt.Sprintf("thinking %d", i)),
				Translated:   fmt.Sprintf("思考 %d", i),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("SaveThinkingTranslation concurrent: %v", err)
	}

	entries, err := LoadThinkingTranslations(dir)
	if err != nil {
		t.Fatalf("LoadThinkingTranslations: %v", err)
	}
	if len(entries) != count {
		t.Fatalf("entries len = %d, want %d", len(entries), count)
	}
	seen := make(map[string]bool, count)
	for _, entry := range entries {
		seen[entry.MessageID] = true
	}
	for i := range count {
		key := fmt.Sprintf("msgidx:%d", i)
		if !seen[key] {
			t.Fatalf("missing entry %s", key)
		}
	}
}

func TestThinkingTranslationsSaveStripsOpenEnvelope(t *testing.T) {
	dir := t.TempDir()
	entry := ThinkingTranslationEntry{
		MessageID:    "msgidx:1",
		BlockIndex:   0,
		TargetLang:   "zh-Hans",
		OriginalHash: ThinkingTranslationOriginalHash("thinking"),
		Translated:   "<TRANSLATION>\n**评估代码路径**\n\n正文",
	}
	if err := SaveThinkingTranslation(dir, entry); err != nil {
		t.Fatalf("SaveThinkingTranslation: %v", err)
	}
	entries, err := LoadThinkingTranslations(dir)
	if err != nil {
		t.Fatalf("LoadThinkingTranslations: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(entries))
	}
	if got, want := entries[0].Translated, "**评估代码路径**\n\n正文"; got != want {
		t.Fatalf("Translated = %q, want %q", got, want)
	}
}

func TestThinkingTranslationsSlotIgnoresTargetLangChange(t *testing.T) {
	dir := t.TempDir()
	base := ThinkingTranslationEntry{
		MessageID:    "msgidx:1",
		BlockIndex:   0,
		TargetLang:   "zh-Hans",
		OriginalHash: ThinkingTranslationOriginalHash("thinking"),
		Translated:   "思考",
	}
	if err := SaveThinkingTranslation(dir, base); err != nil {
		t.Fatalf("SaveThinkingTranslation: %v", err)
	}
	switched := base
	switched.TargetLang = "ja"
	switched.Translated = "考え"
	if err := SaveThinkingTranslation(dir, switched); err != nil {
		t.Fatalf("SaveThinkingTranslation switched: %v", err)
	}
	entries, err := LoadThinkingTranslations(dir)
	if err != nil {
		t.Fatalf("LoadThinkingTranslations: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries len = %d, want 1 — slot must ignore target_lang", len(entries))
	}
}
