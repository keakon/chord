package mcp

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/keakon/chord/internal/config"
)

func (t *HTTPTransport) prepareRequest(ctx context.Context, req *http.Request) (RequestStamp, error) {
	stamp := RequestStamp{ConnectionGeneration: t.connectionGeneration}
	var lease CredentialLease
	if managedAuth(t.target) {
		if endpoint, err := CanonicalEndpoint(t.url); err != nil || endpoint != t.target.Config.URL {
			return stamp, newTransportFailure("mcp http: resource identity changed", SendNotSent, authError(ErrorIdentityChanged))
		}
		if t.auth == nil {
			return stamp, newTransportFailure("mcp http: authorization service unavailable", SendNotSent, authError(ErrorAuthRequired))
		}
		if t.expected.BindingID != "" {
			var err error
			lease, err = t.auth.Acquire(ctx, t.target, t.expected)
			if err != nil {
				return stamp, newTransportFailure("mcp http: credential unavailable", SendNotSent, err)
			}
			stamp.CredentialVersion = lease.version
		} else if t.target.Config.Auth == config.MCPAuthOAuth {
			return stamp, newTransportFailure("mcp http: authorization required", SendNotSent, authError(ErrorAuthRequired))
		}
	}
	t.applyHeaders(req.Header)
	t.applyProtocolHeaders(req.Header)
	t.applyConnectionHeaders(req.Header)
	if lease.token != "" {
		req.Header.Set("Authorization", "Bearer "+lease.token)
		req.GetBody = nil
	}
	return stamp, nil
}

func (t *HTTPTransport) observeResponse(ctx context.Context, resp *http.Response, stamp RequestStamp, method string, params any) error {
	failure := responseHTTPError(resp)
	if t.auth == nil || stamp.BindingID == "" || (resp.StatusCode != 401 && resp.StatusCode != 403) {
		return failure
	}
	challenge := Challenge{StatusCode: resp.StatusCode, Stage: method}
	if method == "tools/call" {
		if object, ok := params.(map[string]any); ok {
			challenge.RemoteTool, _ = object["name"].(string)
		}
	}
	for _, b := range failure.BearerChallenges {
		if b.Error == "insufficient_scope" || b.Error == "invalid_token" {
			challenge.Error = b.Error
			challenge.Scope = b.Scope
			break
		}
	}
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var result error = failure
	if err := t.auth.ObserveRejection(observeCtx, t.target, stamp, challenge); err != nil {
		result = errors.Join(failure, err)
	}
	if t.onAuthResponse != nil {
		t.onAuthResponse(observeCtx, stamp, result)
	}
	return result
}
