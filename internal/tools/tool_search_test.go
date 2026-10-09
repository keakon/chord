package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

type searchInputBackend struct{ queries []string }

func (*searchInputBackend) HasDiscoverableTools() bool { return true }
func (b *searchInputBackend) SearchTools(_ context.Context, query string, _ []string) (message.ToolDiscoveryResult, error) {
	b.queries = append(b.queries, query)
	return message.ToolDiscoveryResult{}, nil
}

func TestToolSearchQueryCharacterLimit(t *testing.T) {
	for _, sample := range []struct{ name, char string }{{"ASCII", "a"}, {"CJK", "文"}, {"supplementary Unicode", "😀"}} {
		for _, length := range []int{1000, 1001} {
			t.Run(sample.name+"/"+strconv.Itoa(length), func(t *testing.T) {
				backend := &searchInputBackend{}
				query := strings.Repeat(sample.char, length)
				raw, err := json.Marshal(map[string]string{"query": query})
				if err != nil {
					t.Fatal(err)
				}
				_, err = NewToolSearchTool(backend).Execute(t.Context(), raw)
				if length == 1000 {
					if err != nil || len(backend.queries) != 1 || backend.queries[0] != query {
						t.Fatalf("valid query rejected: %v", err)
					}
				} else if err == nil || len(backend.queries) != 0 {
					t.Fatalf("oversized query reached backend: %v", err)
				}
			})
		}
	}
}
