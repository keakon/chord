package agent

import (
	"testing"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type releasingHostedProvider struct {
	llm.Provider
	releases int
	msgs     *message.Message
	msgsLen  int
}

func (p *releasingHostedProvider) ReleaseBodyCache(msgs *message.Message, msgsLen int) {
	p.releases++
	p.msgs = msgs
	p.msgsLen = msgsLen
}

func TestHostedRequestProviderForwardsBodyCacheRelease(t *testing.T) {
	inner := &releasingHostedProvider{}
	wrapper := hostedRequestProvider{Provider: inner}
	if _, ok := any(wrapper).(llm.BodyCacheReleaser); !ok {
		t.Fatal("hostedRequestProvider does not satisfy llm.BodyCacheReleaser")
	}
	msgs := []message.Message{{Role: message.RoleUser, Content: "hello"}}
	ptr := &msgs[0]
	wrapper.ReleaseBodyCache(ptr, len(msgs))
	if inner.releases != 1 {
		t.Fatalf("release calls = %d, want 1", inner.releases)
	}
	if inner.msgs != ptr || inner.msgsLen != len(msgs) {
		t.Fatalf("forwarded identity = (%p, %d), want (%p, %d)", inner.msgs, inner.msgsLen, ptr, len(msgs))
	}
}
