package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/keakon/chord/internal/worktree"
)

// ServerRef identifies an owner-visible server independently of runtime instances.
type ServerRef struct {
	WorkspaceID string `json:"workspace_id"`
	Owner       string `json:"owner"`
	ServerName  string `json:"server_name"`
}

const MainOwner = "main"

func identityHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic("MCP identity contains an unsupported value")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ResolveWorkspaceID shares credentials across linked worktrees, but never
// falls back to a different identity when Git cannot be executed.
func ResolveWorkspaceID(ctx context.Context, root string) (string, error) {
	common, err := worktree.GitCommonDir(ctx, root)
	kind := "git"
	if err != nil {
		if !errors.Is(err, worktree.ErrNotGitRepository) {
			return "", fmt.Errorf("resolve MCP workspace: %w", err)
		}
		common, kind = root, "directory"
	}
	absolute, err := filepath.Abs(common)
	if err != nil {
		return "", fmt.Errorf("resolve MCP workspace path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve MCP workspace links: %w", err)
	}
	return identityHash(struct{ Kind, Path string }{kind, canonical}), nil
}

// CanonicalEndpoint normalizes only origin and the empty root path. Query
// bytes and non-root trailing slashes remain part of the resource identity.
func CanonicalEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid MCP endpoint")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || u.Opaque != "" {
		return "", fmt.Errorf("invalid MCP endpoint")
	}
	host := strings.ToLower(u.Hostname())
	for _, r := range host {
		if r > unicode.MaxASCII || unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("invalid MCP endpoint host")
		}
	}
	if host == "" || strings.Contains(host, "%") {
		return "", fmt.Errorf("invalid MCP endpoint host")
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

// ValidateResourceBinding rejects ambiguous paths before comparing segment
// ancestry, so escaped separators cannot broaden a credential's audience.
func ValidateResourceBinding(endpoint, resource string) error {
	e, err := CanonicalEndpoint(endpoint)
	if err != nil {
		return err
	}
	r, err := CanonicalEndpoint(resource)
	if err != nil {
		return err
	}
	eu, _ := url.Parse(e)
	ru, _ := url.Parse(r)
	if eu.Scheme != ru.Scheme || eu.Host != ru.Host || (ru.RawQuery != "" && ru.RawQuery != eu.RawQuery) {
		return fmt.Errorf("MCP resource mismatch")
	}
	for _, u := range []*url.URL{eu, ru} {
		escaped := strings.ToLower(u.EscapedPath())
		if strings.ContainsAny(u.Path, "\\") || strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%25") {
			return fmt.Errorf("ambiguous MCP resource path")
		}
		for segment := range strings.SplitSeq(u.Path, "/") {
			if segment == "." || segment == ".." {
				return fmt.Errorf("ambiguous MCP resource path")
			}
		}
	}
	if eu.EscapedPath() != ru.EscapedPath() && !strings.HasPrefix(eu.EscapedPath(), strings.TrimSuffix(ru.EscapedPath(), "/")+"/") {
		return fmt.Errorf("MCP resource mismatch")
	}
	return nil
}
