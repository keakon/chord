package llm

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func TestProviderConfigCloseWaitsForBackgroundTask(t *testing.T) {
	provider := NewProviderConfig("sample", config.ProviderConfig{}, nil)
	releaseTask := make(chan struct{})
	if !provider.StartBackgroundTask(func() { <-releaseTask }) {
		t.Fatal("StartBackgroundTask returned false before close")
	}

	closeDone := make(chan struct{})
	go func() {
		provider.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
		t.Fatal("Close returned before tracked background task completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseTask)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after tracked background task completed")
	}

	if provider.StartBackgroundTask(func() {}) {
		t.Fatal("StartBackgroundTask returned true after close")
	}
}
