package llm

import (
	"fmt"
	"net/http"

	"github.com/keakon/chord/internal/config"
)

// applyHostedToolHeaders runs after provider overrides on both wire families.
// Validation also protects callers that construct requests without a catalog.
func applyHostedToolHeaders(header http.Header, hosted *HostedToolRequest) error {
	if hosted == nil {
		return nil
	}
	if err := config.ValidateHostedToolHeaders(hosted.Headers); err != nil {
		return fmt.Errorf("hosted tool headers: %w", err)
	}
	for name, value := range hosted.Headers {
		header.Set(name, value)
	}
	return nil
}
