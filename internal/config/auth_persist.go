package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// OAuthCredentialMatch identifies an OAuth credential slot inside auth.yaml.
// AccountID is the only supported selector.
type OAuthCredentialMatch struct {
	AccountID string
}

type authYAMLDocument struct {
	root  yaml.Node
	dirty bool
}

type authCredentialNodeRef struct {
	node         *yaml.Node
	credential   ProviderCredential
	visibleIndex int
}

func UpsertOAuthCredentialInFile(path, provider string, cred *OAuthCredential) (AuthConfig, error) {
	if cred == nil {
		return nil, fmt.Errorf("oauth credential is nil")
	}
	if strings.TrimSpace(cred.AccountID) == "" {
		return nil, fmt.Errorf("oauth credential account_id is required")
	}
	return mutateAuthYAMLFile(path, func(doc *authYAMLDocument) error {
		return doc.upsertOAuthCredential(provider, cred)
	})
}

func UpdateOAuthCredentialInFile(
	path string,
	provider string,
	match OAuthCredentialMatch,
	mutate func(*OAuthCredential) (bool, error),
) (AuthConfig, *OAuthCredential, bool, error) {
	if mutate == nil {
		return nil, nil, false, fmt.Errorf("oauth credential mutate func is nil")
	}
	var (
		updated *OAuthCredential
		changed bool
	)
	auth, err := mutateAuthYAMLFile(path, func(doc *authYAMLDocument) error {
		var updateErr error
		updated, changed, updateErr = doc.updateOAuthCredential(provider, match, mutate)
		return updateErr
	})
	if err != nil {
		return nil, nil, false, err
	}
	return auth, updated, changed, nil
}

func mutateAuthYAMLFile(path string, mutate func(*authYAMLDocument) error) (AuthConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("auth config path is empty")
	}
	if mutate == nil {
		return nil, fmt.Errorf("auth config mutate func is nil")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create auth config dir: %w", err)
	}
	lock, err := lockAuthYAMLFile(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = lock.Close()
	}()

	doc, err := loadAuthYAMLDocument(path)
	if err != nil {
		return nil, err
	}
	if err := mutate(doc); err != nil {
		return nil, err
	}
	if doc.dirty {
		if err := doc.save(path); err != nil {
			return nil, err
		}
	}
	auth, err := doc.authConfig()
	if err != nil {
		return nil, err
	}
	return auth, nil
}

type authYAMLLock struct {
	file *os.File
}

func lockAuthYAMLFile(path string) (*authYAMLLock, error) {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open auth config lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock auth config: %w", err)
	}
	return &authYAMLLock{file: f}, nil
}

func (l *authYAMLLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	var firstErr error
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil && !errors.Is(err, fs.ErrClosed) {
		firstErr = err
	}
	if err := l.file.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	l.file = nil
	return firstErr
}

func loadAuthYAMLDocument(path string) (*authYAMLDocument, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return newEmptyAuthYAMLDocument(), nil
	case err != nil:
		return nil, fmt.Errorf("read auth config: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return newEmptyAuthYAMLDocument(), nil
	}

	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode auth config yaml: %w", err)
	}
	if root.Kind == 0 {
		return newEmptyAuthYAMLDocument(), nil
	}
	if root.Kind != yaml.DocumentNode {
		return nil, fmt.Errorf("auth config root must be a YAML document")
	}
	if len(root.Content) == 0 {
		root.Content = []*yaml.Node{&yaml.Node{Kind: yaml.MappingNode}}
	} else if root.Content[0] == nil {
		root.Content[0] = &yaml.Node{Kind: yaml.MappingNode}
	} else if root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("auth config root must be a mapping")
	}
	return &authYAMLDocument{root: root}, nil
}

func newEmptyAuthYAMLDocument() *authYAMLDocument {
	return &authYAMLDocument{
		root: yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{
				&yaml.Node{Kind: yaml.MappingNode},
			},
		},
	}
}

func (d *authYAMLDocument) save(path string) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&d.root); err != nil {
		_ = enc.Close()
		return fmt.Errorf("encode auth config yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("finalize auth config yaml: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write auth config tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install auth config: %w", err)
	}
	d.dirty = false
	return nil
}

func (d *authYAMLDocument) authConfig() (AuthConfig, error) {
	if d == nil {
		return make(AuthConfig), nil
	}
	root := d.rootMapping()
	var raw map[string][]ProviderCredential
	if err := root.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode auth config: %w", err)
	}
	return normalizeAuthConfig(raw), nil
}

func (d *authYAMLDocument) rootMapping() *yaml.Node {
	if d.root.Kind != yaml.DocumentNode {
		d.root = yaml.Node{Kind: yaml.DocumentNode}
	}
	if len(d.root.Content) == 0 || d.root.Content[0] == nil {
		d.root.Content = []*yaml.Node{&yaml.Node{Kind: yaml.MappingNode}}
	}
	if d.root.Content[0].Kind != yaml.MappingNode {
		d.root.Content[0] = &yaml.Node{Kind: yaml.MappingNode}
	}
	return d.root.Content[0]
}

func normalizeAuthConfig(raw map[string][]ProviderCredential) AuthConfig {
	auth := make(AuthConfig)
	for provider, creds := range raw {
		var filtered []ProviderCredential
		for _, c := range creds {
			if c.APIKey == "" && c.OAuth == nil && !c.ExplicitEmpty {
				continue
			}
			filtered = append(filtered, c)
		}
		if len(filtered) > 0 {
			auth[provider] = filtered
		}
	}
	return auth
}

func (d *authYAMLDocument) providerSequence(provider string, create bool) (*yaml.Node, error) {
	root := d.rootMapping()
	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode := root.Content[i]
		if keyNode.Kind == yaml.ScalarNode && keyNode.Value == provider {
			valueNode := root.Content[i+1]
			if valueNode.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("auth provider %q must be a sequence", provider)
			}
			return valueNode, nil
		}
	}
	if !create {
		return nil, nil
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: provider}
	valueNode := &yaml.Node{Kind: yaml.SequenceNode}
	root.Content = append(root.Content, keyNode, valueNode)
	d.dirty = true
	return valueNode, nil
}

func (d *authYAMLDocument) providerCredentialRefs(provider string) ([]authCredentialNodeRef, *yaml.Node, error) {
	seq, err := d.providerSequence(provider, false)
	if err != nil || seq == nil {
		return nil, seq, err
	}
	refs := make([]authCredentialNodeRef, 0, len(seq.Content))
	visibleIndex := 0
	for _, node := range seq.Content {
		var cred ProviderCredential
		if err := node.Decode(&cred); err != nil {
			return nil, nil, fmt.Errorf("decode auth provider %q credential: %w", provider, err)
		}
		ref := authCredentialNodeRef{
			node:         node,
			credential:   cred,
			visibleIndex: -1,
		}
		if cred.APIKey != "" || cred.OAuth != nil || cred.ExplicitEmpty {
			ref.visibleIndex = visibleIndex
			visibleIndex++
		}
		refs = append(refs, ref)
	}
	return refs, seq, nil
}

func (d *authYAMLDocument) upsertOAuthCredential(provider string, cred *OAuthCredential) error {
	seq, err := d.providerSequence(provider, true)
	if err != nil {
		return err
	}
	refs, _, err := d.providerCredentialRefs(provider)
	if err != nil {
		return err
	}
	if target := findOAuthCredentialUpsertTarget(refs, cred); target != nil {
		if updateOAuthMappingNode(target.node, cred) {
			d.dirty = true
		}
		return nil
	}
	seq.Content = append(seq.Content, newOAuthCredentialNode(cred))
	d.dirty = true
	return nil
}

func findOAuthCredentialUpsertTarget(refs []authCredentialNodeRef, cred *OAuthCredential) *authCredentialNodeRef {
	if cred == nil || cred.AccountID == "" {
		return nil
	}
	for i := range refs {
		if refs[i].credential.OAuth == nil {
			continue
		}
		if refs[i].credential.OAuth.AccountID == cred.AccountID {
			return &refs[i]
		}
	}
	return nil
}

func (d *authYAMLDocument) updateOAuthCredential(
	provider string,
	match OAuthCredentialMatch,
	mutate func(*OAuthCredential) (bool, error),
) (*OAuthCredential, bool, error) {
	refs, _, err := d.providerCredentialRefs(provider)
	if err != nil {
		return nil, false, err
	}
	target := findMatchingOAuthCredentialRef(refs, match)
	if target == nil || target.credential.OAuth == nil {
		return nil, false, fmt.Errorf("oauth credential not found for provider %q", provider)
	}
	updated := *target.credential.OAuth
	changed, err := mutate(&updated)
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return &updated, false, nil
	}
	if updateOAuthMappingNode(target.node, &updated) {
		d.dirty = true
	}
	return &updated, true, nil
}

func findMatchingOAuthCredentialRef(refs []authCredentialNodeRef, match OAuthCredentialMatch) *authCredentialNodeRef {
	for i := range refs {
		if refs[i].credential.OAuth == nil {
			continue
		}
		if match.AccountID != "" && refs[i].credential.OAuth.AccountID == match.AccountID {
			return &refs[i]
		}
	}
	return nil
}
func newOAuthCredentialNode(cred *OAuthCredential) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	updateOAuthMappingNode(node, cred)
	return node
}

func updateOAuthMappingNode(node *yaml.Node, cred *OAuthCredential) bool {
	changed := false
	changed = setMappingString(node, "refresh", cred.Refresh, false) || changed
	changed = setMappingString(node, "access", cred.Access, false) || changed
	changed = setMappingInt64(node, "expires", cred.Expires) || changed
	changed = setMappingString(node, "account_id", cred.AccountID, true) || changed
	changed = setMappingString(node, "email", cred.Email, true) || changed
	changed = setMappingString(node, "status", string(cred.Status), true) || changed
	return changed
}

func setMappingString(node *yaml.Node, key, value string, omitEmpty bool) bool {
	if omitEmpty && value == "" {
		return removeMappingKey(node, key)
	}
	if existing := findMappingValueNode(node, key); existing != nil {
		if existing.Kind == yaml.ScalarNode && existing.Value == value {
			return false
		}
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!str"
		existing.Value = value
		return true
	}
	appendMappingScalar(node, key, "!!str", value)
	return true
}

func setMappingInt64(node *yaml.Node, key string, value int64) bool {
	text := fmt.Sprintf("%d", value)
	if existing := findMappingValueNode(node, key); existing != nil {
		if existing.Kind == yaml.ScalarNode && existing.Value == text {
			return false
		}
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!int"
		existing.Value = text
		return true
	}
	appendMappingScalar(node, key, "!!int", text)
	return true
}

func appendMappingScalar(node *yaml.Node, key, tag, value string) {
	if node.Kind != yaml.MappingNode {
		node.Kind = yaml.MappingNode
		node.Content = nil
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value},
	)
}

func removeMappingKey(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Kind == yaml.ScalarNode && node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return true
		}
	}
	return false
}

func findMappingValueNode(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Kind == yaml.ScalarNode && node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
