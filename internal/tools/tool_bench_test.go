package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

type benchmarkTool struct {
	name string
}

var benchmarkToolSchema = map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}

func (t benchmarkTool) Name() string { return t.name }
func (t benchmarkTool) Description() string {
	return "benchmark tool used to measure registry business paths"
}
func (t benchmarkTool) Parameters() map[string]any                               { return benchmarkToolSchema }
func (t benchmarkTool) Execute(context.Context, json.RawMessage) (string, error) { return "ok", nil }
func (t benchmarkTool) IsReadOnly() bool                                         { return true }

func benchmarkRegistry(size int) (*Registry, []string) {
	r := NewRegistry()
	names := make([]string, size)
	for i := 0; i < size; i++ {
		name := fmt.Sprintf("BenchTool%03d", i)
		names[i] = name
		r.Register(benchmarkTool{name: name})
	}
	return r, names
}

func BenchmarkRegistryGetParallel(b *testing.B) {
	r, names := benchmarkRegistry(128)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = r.Get(names[i&127])
			i++
		}
	})
}

func BenchmarkRegistryListDefinitions(b *testing.B) {
	r, _ := benchmarkRegistry(128)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = r.ListDefinitions()
	}
}

func BenchmarkRegistryExecuteParallel(b *testing.B) {
	r, names := benchmarkRegistry(128)
	args := json.RawMessage(`{"value":"x"}`)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = r.Execute(context.Background(), names[i&127], args)
			i++
		}
	})
}
