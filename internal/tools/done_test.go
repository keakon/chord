package tools

import "testing"

func TestDoneToolParameters(t *testing.T) {
	params := NewDoneTool().Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", params["properties"])
	}
	if _, ok := props["reason"]; !ok {
		t.Fatalf("Done tool parameters missing reason property: %#v", props)
	}
}
