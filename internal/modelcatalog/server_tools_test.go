package modelcatalog

import (
	"testing"

	"github.com/keakon/chord/internal/toolname"
)

func TestServerToolEvidenceAndIsolation(t *testing.T) {
	b := Binding{ModelID: "sample", ServerTools: map[string]ServerToolCapability{"web_search": {State: ServerToolSupported, Contract: "openai.responses.web_search", Evidence: "documentation", Sources: []Source{{URL: "https://example.invalid/docs", Checked: "2026-10-09"}}}}}
	e := Endpoint{Protocol: "responses"}
	if err := validateServerTools(b, e); err == nil {
		t.Fatal("documentation asserted verified support")
	}
	c := b.ServerTools["web_search"]
	c.Evidence = "api"
	b.ServerTools["web_search"] = c
	if err := validateServerTools(b, e); err != nil {
		t.Fatal(err)
	}
	if err := validateServerTools(b, Endpoint{Protocol: "messages"}); err == nil {
		t.Fatal("wrong protocol accepted")
	}
	cloned := cloneBinding(b)
	c = cloned.ServerTools["web_search"]
	c.Sources[0].URL = "https://example.invalid/changed"
	if b.ServerTools["web_search"].Sources[0].URL != "https://example.invalid/docs" {
		t.Fatal("shared evidence mutated")
	}
	p := &ConfigProfile{Model: map[string]any{"native_web_search": map[string]any{"preauthorized": true}}}
	if err := p.validate("sample"); err == nil {
		t.Fatal("portable profile accepted authorization")
	}
}

func TestImageGenerationServerToolEvidence(t *testing.T) {
	capability := ServerToolCapability{State: ServerToolSupported, Contract: ServerToolContractResponsesImageGeneration, Evidence: "api", Sources: []Source{{URL: "https://example.invalid/docs", Checked: "2026-10-09"}}}
	binding := Binding{ModelID: "sample", ServerTools: map[string]ServerToolCapability{toolname.GenerateImage: capability}}
	if err := validateServerTools(binding, Endpoint{Protocol: "responses"}); err != nil {
		t.Fatal(err)
	}
	if err := validateServerTools(binding, Endpoint{Protocol: "messages"}); err == nil {
		t.Fatal("wrong protocol accepted")
	}
	capability.Evidence = "documentation"
	binding.ServerTools[toolname.GenerateImage] = capability
	if err := validateServerTools(binding, Endpoint{Protocol: "responses"}); err == nil {
		t.Fatal("documentation asserted verified image support")
	}
	capability.Evidence, capability.Contract = "api", "unimplemented.image_generation"
	binding.ServerTools[toolname.GenerateImage] = capability
	if err := validateServerTools(binding, Endpoint{Protocol: "responses"}); err == nil {
		t.Fatal("unimplemented contract accepted")
	}
}
