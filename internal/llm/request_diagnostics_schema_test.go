package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestDiagnosticSchemaCacheScope(t *testing.T) {
	defs := []message.ToolDefinition{
		{Name: "sample", InputSchema: map[string]any{"type": "string"}},
		{Name: "sample", InputSchema: map[string]any{"type": "number"}},
	}
	b := newDiagnosticBudget(t.Context())
	first, ok := b.toolSchema(&defs[0])
	if !ok {
		t.Fatal("valid schema rejected")
	}
	second, ok := b.toolSchema(&defs[1])
	if !ok || bytes.Equal(first, second) {
		t.Fatal("distinct schemas with the same name were merged")
	}
	again, ok := b.toolSchema(&defs[0])
	if !ok || !bytes.Equal(first, again) {
		t.Fatal("repeated schema changed")
	}
	defs[0].InputSchema["type"] = "boolean"
	next := newDiagnosticBudget(t.Context())
	changed, ok := next.toolSchema(&defs[0])
	if !ok || bytes.Equal(first, changed) {
		t.Fatal("schema cache crossed request boundary")
	}
}

func TestDiagnosticSchemaCacheBudget(t *testing.T) {
	b := newDiagnosticBudget(t.Context())
	defs := make([]message.ToolDefinition, diagnosticSchemaCacheEntries+1)
	for i := range defs {
		defs[i].InputSchema = map[string]any{"description": strings.Repeat("x", 4096)}
		data, ok := b.toolSchema(&defs[i])
		if !ok || len(data) == 0 {
			t.Fatal("cache capacity changed schema validity")
		}
	}
	if b.schemaBytes > diagnosticSchemaCacheBytes || len(b.schemas) > diagnosticSchemaCacheEntries {
		t.Fatal("schema cache exceeded its budget")
	}
	cyclic := map[string]any{}
	cyclic["cycle"] = cyclic
	d := message.ToolDefinition{InputSchema: cyclic}
	for range 2 {
		if _, ok := b.toolSchema(&d); ok {
			t.Fatal("cyclic schema accepted")
		}
	}
}

func TestDiagnosticHashScratchPreservesFraming(t *testing.T) {
	b := newDiagnosticBudget(t.Context())
	b.deadline = time.Now().Add(time.Hour)
	got, want := sha256.New(), sha256.New()
	for _, s := range []string{"", "sample", strings.Repeat("x", 70<<10), "tail"} {
		if !diagnosticHashField(got, b, s) {
			t.Fatal("hash unexpectedly truncated")
		}
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(len(s)))
		want.Write(size[:])
		want.Write([]byte(s))
	}
	if !bytes.Equal(got.Sum(nil), want.Sum(nil)) {
		t.Fatal("scratch hashing changed field framing")
	}
}
