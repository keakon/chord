package recovery

import (
	"os"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestMessageTailRepairBeforeDurableAppend(t *testing.T) {
	for _, tail := range []string{`{"role":"user","content":"partial`, `{"role":"user","content":"complete"}`} {
		t.Run(tail, func(t *testing.T) {
			rm, dir := newTestManager(t)
			defer rm.Close()
			path := rm.messageLogPath("main")
			if err := os.WriteFile(path, []byte("{\"role\":\"user\",\"content\":\"first\"}\n"+tail), 0600); err != nil {
				t.Fatal(err)
			}
			if err := rm.PersistMessageDurable("main", message.Message{Role: message.RoleUser, Content: "next"}); err != nil {
				t.Fatal(err)
			}
			msgs, err := rm.LoadMessages("main")
			if err != nil {
				t.Fatal(err)
			}
			expected := 2
			if tail[len(tail)-1] == '}' {
				expected = 3
			}
			if len(msgs) != expected || msgs[len(msgs)-1].Content != "next" {
				t.Fatalf("repaired messages: %+v", msgs)
			}
			if err := repairMessageTail(dir, path); err != nil {
				t.Fatal(err)
			}
		})
	}
}
