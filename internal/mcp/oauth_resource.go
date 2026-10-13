package mcp

import (
	"context"
	"errors"
	"net/http"

	"github.com/keakon/chord/internal/config"
)

// resourceChallenge makes a fresh, anonymous handshake against the configured
// resource. Only this endpoint receives its custom headers; discovery and token
// endpoints never receive them. No business call or credential is replayed.
func (s *AuthService) resourceChallenge(ctx context.Context, target Target) ([]BearerChallenge, error) {
	target = target.Snapshot()
	target.Config.Auth = config.MCPAuthNone
	client, err := createTargetClient(ctx, target, s, s.clientInfo, ExpectedIdentity{}, 0)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	// A discovery handshake must not delegate trust or custom headers to a
	// redirect destination, even before a bearer token exists.
	client.transport.(*HTTPTransport).client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	err = client.Initialize(ctx)
	if err == nil {
		return nil, nil
	}
	if response, ok := errors.AsType[*HTTPError](err); ok && response.StatusCode == http.StatusUnauthorized {
		if response.SafeSummary == httpInvalidChallengeSummary {
			return nil, authError(ErrorProtocol)
		}
		return response.BearerChallenges, nil
	}
	return nil, err
}
