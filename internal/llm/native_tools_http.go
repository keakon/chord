package llm

import (
	"fmt"
	"net/http"
)

// nativeToolHTTPClient binds execution to the preauthorized endpoint. Copying
// the client keeps this policy local to the request and shares its transport.
func nativeToolHTTPClient(client *http.Client, native bool) *http.Client {
	if !native {
		return client
	}
	bound := *client
	bound.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fmt.Errorf("native tool request redirect refused: endpoint is not preauthorized")
	}
	return &bound
}
