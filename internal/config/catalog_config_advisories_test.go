package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogConfigAdvisoriesTrackExplicitProfileLeaves(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        reasoning:
          summary: none
        compaction:
          threshold: 0.8
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	advisories := CatalogConfigAdvisories(rc)
	if len(advisories) != 2 {
		t.Fatalf("advisories = %+v, want reasoning.summary and compaction.threshold", advisories)
	}
	if advisories[0].Path != "providers.openai.models.gpt-6.1-sol.reasoning.summary" ||
		advisories[0].Current != "none" ||
		advisories[0].Recommended != "auto" {
		t.Fatalf("first advisory = %+v", advisories[0])
	}
	if !advisories[0].CanPin || !advisories[0].CanFollowCatalog {
		t.Fatalf("first advisory write modes = %+v", advisories[0])
	}
	if advisories[1].Path != "providers.openai.models.gpt-6.1-sol.compaction.threshold" ||
		advisories[1].Current != 0.8 ||
		advisories[1].Recommended != 0.25 {
		t.Fatalf("second advisory = %+v", advisories[1])
	}
	logLines := CatalogConfigAdvisoryLogMessages(CatalogConfigAdvisories(rc))
	if len(logLines) != 2 {
		t.Fatalf("log lines = %+v, want one per directly writable declaration", logLines)
	}
	wantLine := advisories[0].Path + ` differs from verified catalog profile "openai/gpt-6.1-sol" (current "none", recommended "auto"); declared at ` + advisories[0].OriginRef()
	if logLines[0] != wantLine {
		t.Fatalf("log line = %q, want %q", logLines[0], wantLine)
	}
}

func TestCatalogConfigAdvisoryFollowCatalogRequiresAWholeBlockOverride(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        reasoning:
          effort: low
          summary: none
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	advisories := CatalogConfigAdvisories(rc)
	if len(advisories) != 1 || advisories[0].CanFollowCatalog {
		t.Fatalf("advisories = %+v, want pin-only partial block recommendation", advisories)
	}
}

func TestCatalogConfigAdvisoryFollowCatalogDoesNotDropLowerLayerOverride(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        reasoning: {summary: concise}
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	project := writeCatalogTestConfig(t, "project.yaml", `providers:
  openai:
    models:
      gpt-6.1-sol:
        reasoning: {summary: none}
`)
	rc, err := LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	advisories := CatalogConfigAdvisories(rc)
	if len(advisories) != 1 || advisories[0].Current != "none" || advisories[0].CanFollowCatalog {
		t.Fatalf("advisories = %+v, want pin-only project override with lower explicit value", advisories)
	}
}

func TestCatalogConfigAdvisoriesIgnoreMaterializedValues(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	if got := CatalogConfigAdvisories(rc); got != nil {
		t.Fatalf("catalog-filled values must not appear as user overrides: %+v", got)
	}
	if got := CatalogConfigAdvisorySummary(CatalogConfigAdvisories(rc)); got != "" {
		t.Fatalf("summary = %q, want empty when nothing is outstanding", got)
	}
	if got := CatalogConfigAdvisoryLogMessages(CatalogConfigAdvisories(rc)); got != nil {
		t.Fatalf("log lines = %+v, want none when nothing is outstanding", got)
	}
}

func TestCatalogConfigAdvisoryAcknowledgmentUsesValueFingerprint(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        reasoning: {summary: none}
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatal(err)
	}
	advisories := CatalogConfigAdvisories(rc)
	if len(advisories) != 1 {
		t.Fatalf("advisories = %+v", advisories)
	}
	if ackErr := RecordCatalogConfigAdvisoryAcknowledgments(advisories[0]); ackErr != nil {
		t.Fatal(ackErr)
	}
	if got := CatalogConfigAdvisories(rc); got != nil {
		t.Fatalf("acknowledged advisory = %+v", got)
	}
	data, readErr := os.ReadFile(filepath.Join(os.Getenv("CHORD_CONFIG_HOME"), "model-config-advisories.json"))
	if readErr != nil || !strings.Contains(string(data), advisories[0].CurrentFingerprint) {
		t.Fatalf("ack file = %s, err=%v", data, readErr)
	}
	var state catalogConfigAdvisoryAckFile
	if decodeErr := json.Unmarshal(data, &state); decodeErr != nil || len(state.Acks) != 1 {
		t.Fatalf("ack state = %+v, err=%v", state, decodeErr)
	}
	if err := RecordCatalogConfigAdvisoryAcknowledgments(); err != nil {
		t.Fatalf("empty acknowledgment: %v", err)
	}
	if err := RecordCatalogConfigAdvisoryAcknowledgments(advisories[0]); err != nil {
		t.Fatalf("repeated acknowledgment: %v", err)
	}
	repeated, readErr := os.ReadFile(filepath.Join(os.Getenv("CHORD_CONFIG_HOME"), "model-config-advisories.json"))
	if readErr != nil || !bytes.Equal(repeated, data) {
		t.Fatalf("repeated acknowledgment rewrote the state: %s, err=%v", repeated, readErr)
	}

	changedGlobal := writeCatalogTestConfig(t, "changed.yaml", `providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        reasoning: {summary: concise}
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	changed, err := LoadResolvedConfig(changedGlobal, "")
	if err != nil {
		t.Fatal(err)
	}
	changedAdvisories := CatalogConfigAdvisories(changed)
	if len(changedAdvisories) != 1 || changedAdvisories[0].Current != "concise" {
		t.Fatalf("changed advisories = %+v, want the changed current value to re-arm", changedAdvisories)
	}
}

func TestCatalogConfigAdvisoryMessageNamesInheritedSource(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        <<: &shared
          compaction: {threshold: 0.8}
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	advisories := CatalogConfigAdvisories(rc)
	if len(advisories) != 1 {
		t.Fatalf("advisories = %+v, want the inherited compaction threshold", advisories)
	}
	if advisories[0].CanPin || advisories[0].CanFollowCatalog {
		t.Fatalf("inherited value must not be directly writable: %+v", advisories[0])
	}
	msgs := CatalogConfigAdvisoryMessages(CatalogConfigAdvisories(rc))
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
	if !strings.Contains(msgs[0], "inherited through YAML from "+advisories[0].OriginRef()) ||
		!strings.Contains(msgs[0], "set 0.25 there to change every binding that inherits it") ||
		!strings.Contains(msgs[0], "expand the inherited entry to add an explicit override") {
		t.Fatalf("message must name the declaring source and both adopt paths: %s", msgs[0])
	}
	if strings.Count(msgs[0], advisories[0].Path) != 1 {
		t.Fatalf("message must name the offending path once: %s", msgs[0])
	}
	logLines := CatalogConfigAdvisoryLogMessages(CatalogConfigAdvisories(rc))
	if len(logLines) != 1 {
		t.Fatalf("log lines = %+v, want one compact line", logLines)
	}
	wantLine := advisories[0].Path + ` differs from verified catalog profile "openai/gpt-6.1-sol" (current 0.8, recommended 0.25); inherited through YAML from ` + advisories[0].OriginRef()
	if logLines[0] != wantLine {
		t.Fatalf("log line = %q, want %q", logLines[0], wantLine)
	}
	wantSummary := `1 catalog configuration recommendation outstanding; run "chord config advise" to apply the recommended value or keep the current one`
	if got := CatalogConfigAdvisorySummary(CatalogConfigAdvisories(rc)); got != wantSummary {
		t.Fatalf("summary = %q, want %q", got, wantSummary)
	}
}

func TestCatalogConfigAdvisorySharesDeclarationGuards(t *testing.T) {
	shared := CatalogConfigAdvisory{
		Provider: "openai", Model: "model-1", Field: "compaction.threshold",
		CatalogID: "openai/model-1", CatalogVersion: "v1",
		CurrentFingerprint:     "current",
		RecommendedFingerprint: "recommended",
		CurrentOrigin:          CatalogAdvisoryOrigin{File: "config.yaml", Line: 3, Col: 7},
	}
	if !shared.sharesDeclaration(shared) {
		t.Fatal("identical inherited declarations must group")
	}
	writable := shared
	writable.CanPin = true
	if shared.sharesDeclaration(writable) {
		t.Fatal("a directly writable declaration must not group")
	}
	anonymous := shared
	anonymous.CurrentOrigin.File = ""
	if shared.sharesDeclaration(anonymous) {
		t.Fatal("a declaration without a file must not group")
	}
}

func TestCatalogConfigAdvisoryBindingListTruncatesLongGroups(t *testing.T) {
	group := make([]CatalogConfigAdvisory, 0, 6)
	for i := range 6 {
		group = append(group, CatalogConfigAdvisory{Provider: fmt.Sprintf("openai%d", i), Model: "model-1"})
	}
	want := "openai0/model-1, openai1/model-1, openai2/model-1, openai3/model-1, and 2 more"
	if got := CatalogConfigAdvisoryBindingList(group); got != want {
		t.Fatalf("binding list = %q, want %q", got, want)
	}
}

func TestCatalogConfigAdvisoryGroupsSharedDeclaration(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	global := writeCatalogTestConfig(t, "config.yaml", `model_templates:
  "gpt-base": &gpt-base
    compaction:
      threshold: 0.8
  "gpt-models": &gpt-models
    gpt-6.1-sol:
      <<: *gpt-base
providers:
  openai:
    preset: openai
    models: *gpt-models
  openai2:
    preset: openai
    models: *gpt-models
model_pools:
  default: [openai/gpt-6.1-sol, openai2/gpt-6.1-sol]
`)
	rc, err := LoadResolvedConfig(global, "")
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	advisories := CatalogConfigAdvisories(rc)
	if len(advisories) != 2 {
		t.Fatalf("advisories = %+v, want one per provider", advisories)
	}
	for _, advisory := range advisories {
		if advisory.Field != "compaction.threshold" || advisory.CanPin || advisory.CanFollowCatalog {
			t.Fatalf("advisory = %+v, want a threshold inherited through the shared template", advisory)
		}
	}
	if ref := advisories[0].OriginRef(); ref == "" || ref != advisories[1].OriginRef() {
		t.Fatalf("origins = %q and %q, want the same declaration", advisories[0].OriginRef(), advisories[1].OriginRef())
	}
	groups := CatalogConfigAdvisoryGroups(advisories)
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("groups = %+v, want one group of two bindings", groups)
	}
	if group := CatalogConfigAdvisoryGroup(advisories, advisories[1]); len(group) != 2 {
		t.Fatalf("group for the second binding = %+v", group)
	}
	msgs := CatalogConfigAdvisoryMessages(CatalogConfigAdvisories(rc))
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want one grouped message", msgs)
	}
	for _, want := range []string{"declared once at " + advisories[0].OriginRef(), "shared by 2 bindings", "edit that line to 0.25 to change every binding that inherits it", "keep all 2"} {
		if !strings.Contains(msgs[0], want) {
			t.Fatalf("message = %q, want %q", msgs[0], want)
		}
	}
	if strings.Contains(msgs[0], "openai/gpt-6.1-sol, openai2") {
		t.Fatalf("message must leave the binding list to the grouped listing: %s", msgs[0])
	}
	logLines := CatalogConfigAdvisoryLogMessages(CatalogConfigAdvisories(rc))
	if len(logLines) != 1 {
		t.Fatalf("log lines = %+v, want one per declaration", logLines)
	}
	wantLine := advisories[0].Path + ` differs from verified catalog profile "openai/gpt-6.1-sol" (current 0.8, recommended 0.25); declared once at ` + advisories[0].OriginRef() + ", shared by 2 bindings"
	if logLines[0] != wantLine {
		t.Fatalf("log line = %q, want %q", logLines[0], wantLine)
	}
	for _, command := range []string{"chord config advise", "--keep-current", "--accept"} {
		if strings.Contains(logLines[0], command) {
			t.Fatalf("log line must leave %q to the CLI: %s", command, logLines[0])
		}
	}
	if ackErr := RecordCatalogConfigAdvisoryAcknowledgments(CatalogConfigAdvisoryGroup(advisories, advisories[0])...); ackErr != nil {
		t.Fatal(ackErr)
	}
	if got := CatalogConfigAdvisories(rc); got != nil {
		t.Fatalf("advisories after grouped acknowledgment = %+v", got)
	}
	data, readErr := os.ReadFile(filepath.Join(os.Getenv("CHORD_CONFIG_HOME"), "model-config-advisories.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var state catalogConfigAdvisoryAckFile
	if decodeErr := json.Unmarshal(data, &state); decodeErr != nil || len(state.Acks) != 2 {
		t.Fatalf("ack state = %+v, err=%v, want two entries", state, decodeErr)
	}
}

func TestCatalogConfigAdvisoriesDoNotGroupDistinctDeclarations(t *testing.T) {
	t.Run("inherited from different anchors", func(t *testing.T) {
		t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
		global := writeCatalogTestConfig(t, "config.yaml", `model_templates:
  "base-a": &base-a
    compaction: {threshold: 0.8}
  "base-b": &base-b
    compaction: {threshold: 0.8}
providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        <<: *base-a
  openai2:
    preset: openai
    models:
      gpt-6.1-sol:
        <<: *base-b
model_pools:
  default: [openai/gpt-6.1-sol, openai2/gpt-6.1-sol]
`)
		rc, err := LoadResolvedConfig(global, "")
		if err != nil {
			t.Fatalf("LoadResolvedConfig: %v", err)
		}
		advisories := CatalogConfigAdvisories(rc)
		if len(advisories) != 2 || advisories[0].CanPin || advisories[1].CanPin {
			t.Fatalf("advisories = %+v, want two inherited advisories", advisories)
		}
		if advisories[0].OriginRef() == advisories[1].OriginRef() {
			t.Fatalf("origins = %q, want distinct declarations", advisories[0].OriginRef())
		}
		if groups := CatalogConfigAdvisoryGroups(advisories); len(groups) != 2 {
			t.Fatalf("groups = %+v, want one per declaration", groups)
		}
		if msgs := CatalogConfigAdvisoryMessages(CatalogConfigAdvisories(rc)); len(msgs) != 2 {
			t.Fatalf("messages = %+v, want one per declaration", msgs)
		}
		wantSummary := `2 catalog configuration recommendations outstanding; run "chord config advise" to apply the recommended values or keep the current ones`
		if got := CatalogConfigAdvisorySummary(CatalogConfigAdvisories(rc)); got != wantSummary {
			t.Fatalf("summary = %q, want %q", got, wantSummary)
		}
		if ackErr := RecordCatalogConfigAdvisoryAcknowledgments(CatalogConfigAdvisoryGroup(advisories, advisories[0])...); ackErr != nil {
			t.Fatal(ackErr)
		}
		if got := CatalogConfigAdvisories(rc); len(got) != 1 || got[0].Provider != "openai2" {
			t.Fatalf("advisories after one acknowledgment = %+v, want only the other declaration left", got)
		}
	})

	t.Run("written directly per provider", func(t *testing.T) {
		t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
		global := writeCatalogTestConfig(t, "config.yaml", `providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        compaction: {threshold: 0.8}
  openai2:
    preset: openai
    models:
      gpt-6.1-sol:
        compaction: {threshold: 0.8}
model_pools:
  default: [openai/gpt-6.1-sol, openai2/gpt-6.1-sol]
`)
		rc, err := LoadResolvedConfig(global, "")
		if err != nil {
			t.Fatalf("LoadResolvedConfig: %v", err)
		}
		advisories := CatalogConfigAdvisories(rc)
		if len(advisories) != 2 || !advisories[0].CanPin || !advisories[1].CanPin {
			t.Fatalf("advisories = %+v, want two directly writable advisories", advisories)
		}
		if groups := CatalogConfigAdvisoryGroups(advisories); len(groups) != 2 {
			t.Fatalf("groups = %+v, want one per binding", groups)
		}
	})
}
