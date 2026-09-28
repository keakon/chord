package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/keakon/golog/log"
	powernap "github.com/keakon/x/powernap/pkg/lsp"
	"github.com/keakon/x/powernap/pkg/lsp/protocol"
)

const methodTextDocumentDidSave = "textDocument/didSave"

// saveOptions reports whether the server wants textDocument/didSave and whether
// the notification should carry the document text. The static capability from
// initialize is decoded once; a server that declared nothing statically may
// still register didSave dynamically (the client advertises
// synchronization.dynamicRegistration). Only registrations matching the saved
// document count.
func (c *Client) saveOptions(path string) (protocol.SaveOptions, bool) {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	if !c.staticSaveKnown {
		c.staticSave, c.staticSaveOK = c.client.SaveOptions()
		c.staticSaveKnown = true
	}
	options, requested := c.staticSave, c.staticSaveOK
	for _, dynamic := range c.dynamicSaves {
		if !dynamic.matches(path) {
			continue
		}
		requested = true
		options.IncludeText = options.IncludeText || dynamic.IncludeText
	}
	return options, requested
}

// NotifyDidSave sends textDocument/didSave when the server asked for save
// notifications, statically during initialize or through a dynamic
// registration; other servers are never notified. The document text is
// included only when the server asked for it via SaveOptions.IncludeText.
func (c *Client) NotifyDidSave(ctx context.Context, path string, content string) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closed {
		return nil
	}
	options, requested := c.saveOptions(path)
	if !requested {
		return nil
	}
	var text *string
	if options.IncludeText {
		text = &content
	}
	return c.client.NotifyDidSaveTextDocument(ctx, c.pathToURI(path), text)
}

// handleRegisterCapability records textDocument/didSave registrations and
// accepts every other registration without acting on it: Chord only pushes
// document sync and consumes pushed diagnostics, so the remaining dynamic
// capabilities have no client-side behavior to switch on.
func (c *Client) handleRegisterCapability(_ context.Context, _ string, params json.RawMessage) (any, error) {
	var par struct {
		Registrations []struct {
			ID              string          `json:"id"`
			Method          string          `json:"method"`
			RegisterOptions json.RawMessage `json:"registerOptions"`
		} `json:"registrations"`
	}
	if err := json.Unmarshal(params, &par); err != nil {
		log.Debugf("lsp: unmarshal registerCapability name=%v error=%v", c.name, err)
		return nil, nil
	}
	for _, reg := range par.Registrations {
		if reg.Method != methodTextDocumentDidSave {
			continue
		}
		var options saveRegistration
		if len(reg.RegisterOptions) > 0 {
			// Invalid selectors must never widen a registration to every file.
			if err := json.Unmarshal(reg.RegisterOptions, &options); err != nil {
				log.Debugf("lsp: invalid didSave registration name=%v error=%v", c.name, err)
				continue
			}
		}
		c.saveMu.Lock()
		if c.dynamicSaves == nil {
			c.dynamicSaves = make(map[string]saveRegistration)
		}
		c.dynamicSaves[reg.ID] = options
		c.saveMu.Unlock()
	}
	return nil, nil
}

func (c *Client) handleUnregisterCapability(_ context.Context, _ string, params json.RawMessage) (any, error) {
	var par protocol.UnregistrationParams
	if err := json.Unmarshal(params, &par); err != nil {
		log.Debugf("lsp: unmarshal unregisterCapability name=%v error=%v", c.name, err)
		return nil, nil
	}
	c.saveMu.Lock()
	for _, unreg := range par.Unregisterations {
		if unreg.Method == methodTextDocumentDidSave {
			delete(c.dynamicSaves, unreg.ID)
		}
	}
	c.saveMu.Unlock()
	return nil, nil
}

// handleDiagnosticRefresh answers workspace/diagnostic/refresh with the null
// result the request expects. Chord does not advertise refreshSupport and
// consumes pushed diagnostics only, so there is nothing to re-pull; the
// handler exists so a server that sends the request anyway gets a reply
// instead of a method-not-found error.
func handleDiagnosticRefresh(context.Context, string, json.RawMessage) (any, error) {
	return nil, nil
}

// saveRegistration decodes the LSP 3.17 text-document save registration.
// A null selector uses the client-side routing already applied by Manager;
// an empty selector matches no documents. Notebook filters do not match the
// ordinary file documents Chord opens.
type saveRegistration struct {
	protocol.SaveOptions
	DocumentSelector []saveDocumentFilter `json:"documentSelector"`
}

type saveDocumentFilter struct {
	Language string          `json:"language,omitempty"`
	Scheme   string          `json:"scheme,omitempty"`
	Pattern  *string         `json:"pattern,omitempty"`
	Notebook json.RawMessage `json:"notebook,omitempty"`
}

func (r saveRegistration) matches(path string) bool {
	if r.DocumentSelector == nil {
		return true
	}
	language := string(powernap.DetectLanguage(path))
	if language == "" {
		language = "plaintext"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, filter := range r.DocumentSelector {
		if len(filter.Notebook) > 0 {
			continue
		}
		if filter.Language != "" && filter.Language != "*" && filter.Language != language {
			continue
		}
		// pathToURI always produces a file URI.
		if filter.Scheme != "" && filter.Scheme != "*" && filter.Scheme != "file" {
			continue
		}
		if filter.Pattern != nil {
			matched, err := doublestar.Match(*filter.Pattern, filepath.ToSlash(absolute))
			if err != nil || !matched {
				continue
			}
		}
		if filter.Language != "" || filter.Scheme != "" || filter.Pattern != nil {
			return true
		}
	}
	return false
}
