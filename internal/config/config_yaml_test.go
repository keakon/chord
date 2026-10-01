package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNestedMergeSourceIndexPreservesExplicitNull(t *testing.T) {
	for _, override := range []string{"<<: *base\n        thinking: null", "thinking: null\n        <<: *base"} {
		data := []byte(`providers:
  sample:
    models:
      base: &base
        thinking: {type: enabled, budget: 1024}
      cleared: &cleared
        ` + override + `
      model-1:
        <<: *cleared
`)
		var decoded Config
		if err := yaml.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Providers["sample"].Models["model-1"].Thinking != nil {
			t.Fatal("decoder did not clear the inherited block")
		}
		idx, err := BuildSourceIndex(ConfigLayer{Layer: OriginLayerGlobal, File: "config.yaml", Data: data})
		if err != nil {
			t.Fatal(err)
		}
		decls, ok := idx.ModelBlock("sample", "model-1", "thinking")
		if !ok || !BlockClearing(decls).Cleared {
			t.Fatalf("source index lost explicit clear: %+v", decls)
		}
	}
}

func TestYAMLMappingEntriesMergeOrderAndLiteralKey(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(`first: &first {value: first, inherited: base}
second: &second {value: second}
result:
  <<: [*first, *second]
  inherited: explicit
  "<<": literal
`), &root); err != nil {
		t.Fatal(err)
	}
	entries, err := YAMLMappingEntries(root.Content[0].Content[5])
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"value": "first", "inherited": "explicit", "<<": "literal"}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v", entries)
	}
	for _, entry := range entries {
		if entry.Val.Value != want[entry.Key] || entry.ViaMerge != (entry.Key == "value") {
			t.Fatalf("entry = %+v", entry)
		}
	}
}

func TestYAMLMappingEntriesRejectsRecursiveMerge(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("&self {<<: *self}\n"), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := YAMLMappingEntries(root.Content[0]); err == nil {
		t.Fatal("recursive merge was accepted")
	}
}
