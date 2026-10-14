package llm

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

// --- GetRetryDelay ---

func TestGetRetryDelay(t *testing.T) {
	p := newTestProviderConfig(nil)

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 0},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second},
		{7, 60 * time.Second},
		{35, 60 * time.Second},
	}

	for _, tt := range tests {
		got := p.GetRetryDelay(tt.attempt)
		if got != tt.want {
			t.Errorf("GetRetryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestGetRetryDelay_Fixed(t *testing.T) {
	tests := []struct {
		name     string
		delayMS  int
		want     time.Duration
		attempts int
	}{
		{"default 1s", 0, time.Second, 5},
		{"custom 500ms", 500, 500 * time.Millisecond, 5},
		{"custom 2s", 2000, 2 * time.Second, 5},
		{"maximum 60s", config.MaxProviderRetryDelayMS, time.Minute, 5},
		{"defensive clamp", config.MaxProviderRetryDelayMS + 1, time.Minute, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProviderConfig("test", config.ProviderConfig{
				Type:         config.ProviderTypeChatCompletions,
				Models:       map[string]config.ModelConfig{},
				RetryBackoff: config.RetryBackoffFixed,
				RetryDelayMS: new(tt.delayMS),
			}, nil)
			for i := 1; i <= tt.attempts; i++ {
				got := p.GetRetryDelay(i)
				if got != tt.want {
					t.Errorf("GetRetryDelay(%d) = %v, want %v", i, got, tt.want)
				}
			}
		})
	}
}

func TestGetRetryDelay_ExponentialSubSecondBase(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		Models:       map[string]config.ModelConfig{},
		RetryBackoff: config.RetryBackoffExponential,
		RetryDelayMS: new(10),
	}, nil)
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 10 * time.Millisecond},
		{2, 20 * time.Millisecond},
		{3, 40 * time.Millisecond},
		{4, 80 * time.Millisecond},
		{5, 160 * time.Millisecond},
		{6, 320 * time.Millisecond},
		{7, 640 * time.Millisecond},
		{8, 1280 * time.Millisecond},
		{20, time.Minute},
	}
	for _, tt := range tests {
		if got := p.GetRetryDelay(tt.attempt); got != tt.want {
			t.Errorf("GetRetryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestGetRetryDelay_None(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		Models:       map[string]config.ModelConfig{},
		RetryBackoff: config.RetryBackoffNone,
	}, nil)
	for i := 1; i <= 10; i++ {
		got := p.GetRetryDelay(i)
		if got != 0 {
			t.Errorf("GetRetryDelay(%d) = %v, want 0", i, got)
		}
	}
}

func TestGetRetryDelay_ExponentialCustomBase(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{
		Type:         config.ProviderTypeChatCompletions,
		Models:       map[string]config.ModelConfig{},
		RetryBackoff: config.RetryBackoffExponential,
		RetryDelayMS: new(2000),
	}, nil)
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 0},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 32 * time.Second},
		{6, 60 * time.Second},
		{7, 60 * time.Second},
	}
	for _, tt := range tests {
		got := p.GetRetryDelay(tt.attempt)
		if got != tt.want {
			t.Errorf("GetRetryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestGetRetryDelay_DefaultBackoffWhenEmpty(t *testing.T) {
	// Ensure the default retry_backoff "" uses exponential with 1s base.
	p := NewProviderConfig("test", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{},
	}, nil)
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 0},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second},
		{7, 60 * time.Second},
	}
	for _, tt := range tests {
		got := p.GetRetryDelay(tt.attempt)
		if got != tt.want {
			t.Errorf("GetRetryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}
