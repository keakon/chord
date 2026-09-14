package llm

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

// The body cache is only safe because a request surface is rebuilt rather than
// mutated, so the identity must distinguish the two cases that would otherwise
// reuse a stale body: a slice resliced to a different length (same backing
// array, so the pointer alone matches) and a freshly built slice with equal
// contents (different backing array).
func TestRequestBodyIdentityDistinguishesResliceAndRebuild(t *testing.T) {
	msgs := []message.Message{{Role: message.RoleUser, Content: "a"}, {Role: message.RoleUser, Content: "b"}}
	tools := []message.ToolDefinition{{Name: "read"}}

	base := requestBodyIdentityFor("sys", msgs, tools, 1024)
	if base != requestBodyIdentityFor("sys", msgs, tools, 1024) {
		t.Fatal("identity differs for the same slices, want a hit")
	}

	if base == requestBodyIdentityFor("sys", msgs[:1], tools, 1024) {
		t.Fatal("a resliced prefix matched; the length must be compared alongside the pointer")
	}

	rebuilt := append([]message.Message(nil), msgs...)
	if base == requestBodyIdentityFor("sys", rebuilt, tools, 1024) {
		t.Fatal("a rebuilt slice with equal contents matched; identity must be the backing array, not the values")
	}

	if base == requestBodyIdentityFor("other", msgs, tools, 1024) {
		t.Fatal("a different system prompt matched")
	}
	if base == requestBodyIdentityFor("sys", msgs, tools, 2048) {
		t.Fatal("a different max_tokens matched")
	}
	if base == requestBodyIdentityFor("sys", msgs, nil, 1024) {
		t.Fatal("dropping the tool surface matched")
	}
}

// An empty slice has no first element to point at; two empty surfaces must
// still compare equal rather than depending on whether the caller passed nil
// or an allocated-but-empty slice.
func TestRequestBodyIdentityTreatsEmptySlicesAlike(t *testing.T) {
	if requestBodyIdentityFor("sys", nil, nil, 1024) != requestBodyIdentityFor("sys", []message.Message{}, []message.ToolDefinition{}, 1024) {
		t.Fatal("nil and empty surfaces produced different identities")
	}
}
