package input

import "github.com/keakon/chord/internal/message"

const (
	diagnosticSchemaCacheEntries = 256
	diagnosticSchemaCacheBytes   = 256 << 10
)

type diagnosticSchema struct {
	data  []byte
	valid bool
}

// Definitions are immutable during one provider observation. Key by their
// addresses rather than tool names: historical MCP definitions can reuse names
// with different schemas. The budget owns this bounded cache for one attempt.
func (b *diagnosticBudgetState) toolSchema(d *message.ToolDefinition) ([]byte, bool) {
	if cached, ok := b.schemas[d]; ok {
		return cached.data, cached.valid
	}
	data, valid := boundedDiagnosticJSON(d.InputSchema)
	if len(b.schemas) < diagnosticSchemaCacheEntries && len(data) <= diagnosticSchemaCacheBytes-b.schemaBytes {
		if b.schemas == nil {
			b.schemas = make(map[*message.ToolDefinition]diagnosticSchema)
		}
		b.schemas[d] = diagnosticSchema{data: data, valid: valid}
		b.schemaBytes += len(data)
	}
	return data, valid
}
