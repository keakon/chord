package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// OAuthCredentialMatch identifies an OAuth credential slot inside auth.yaml.
// AccountID is the primary selector. Access and CredentialIndex disambiguate
// matching OAuth slots when multiple entries share the same account_id.
// CredentialIndex uses the normalized AuthConfig index, after filtering unset
// environment-variable credentials the same way LoadAuthConfig does.
type OAuthCredentialMatch struct {
	AccountID       string
	Access          string
	CredentialIndex *int
}

type RemovedOAuthCredentialEntry struct {
	Provider        string
	CredentialIndex int
	AccountID       string
	Email           string
	Access          string
	Status          OAuthCredentialStatus
}

func (e RemovedOAuthCredentialEntry) DisplayName() string {
	if email := strings.TrimSpace(e.Email); email != "" {
		return email
	}
	if accountID := strings.TrimSpace(e.AccountID); accountID != "" {
		return accountID
	}
	if access := strings.TrimSpace(e.Access); access != "" {
		return access
	}
	return fmt.Sprintf("%s[%d]", strings.TrimSpace(e.Provider), e.CredentialIndex)
}

type authYAMLDocument struct {
	root  yaml.Node
	dirty bool
}

type authCredentialNodeRef struct {
	node            *yaml.Node
	credential      ProviderCredential
	normalizedIndex int
}

func UpsertAPIKeyCredentialInFile(path, provider, value string) (bool, error) {
	if strings.TrimSpace(provider) == "" {
		return false, fmt.Errorf("provider name is empty")
	}
	if strings.TrimSpace(value) == "" {
		return false, fmt.Errorf("api key value is empty")
	}
	var changed bool
	_, err := mutateAuthYAMLFile(path, func(doc *authYAMLDocument) error {
		var innerChanged bool
		var innerErr error
		innerChanged, innerErr = doc.upsertAPIKeyCredential(provider, value)
		changed = innerChanged
		return innerErr
	})
	if err != nil {
		return false, err
	}
	return changed, nil
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
	if updated != nil {
		if statePath, stateErr := AuthStatePath(); stateErr == nil {
			if state, loadErr := LoadAuthState(statePath); loadErr == nil {
				if stateCred, ok := findMatchingOAuthStateRecord(state, provider, updated); ok {
					if stateCred.Status != "" {
						updated.Status = stateCred.Status
					}
					updated.CodexPrimaryResetAt = stateCred.CodexPrimaryResetAt
					updated.CodexSecondaryResetAt = stateCred.CodexSecondaryResetAt
				}
			}
		}
	}
	return auth, updated, changed, nil
}

func RemoveOAuthCredentialsInFile(path string, remove func(provider string, cred *OAuthCredential, normalizedIndex int) bool) (AuthConfig, []RemovedOAuthCredentialEntry, error) {
	if remove == nil {
		return nil, nil, fmt.Errorf("oauth credential remove func is nil")
	}
	var removed []RemovedOAuthCredentialEntry
	auth, err := mutateAuthYAMLFile(path, func(doc *authYAMLDocument) error {
		var removeErr error
		removed, removeErr = doc.removeOAuthCredentials(remove)
		return removeErr
	})
	if err != nil {
		return nil, nil, err
	}
	return auth, removed, nil
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
	if err := writeAuthYAMLFile(path, buf.Bytes()); err != nil {
		return err
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
			if !isRetainedProviderCredential(c) {
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
	normalizedIndex := 0
	for _, node := range seq.Content {
		var cred ProviderCredential
		if err := node.Decode(&cred); err != nil {
			return nil, nil, fmt.Errorf("decode auth provider %q credential: %w", provider, err)
		}
		ref := authCredentialNodeRef{
			node:            node,
			credential:      cred,
			normalizedIndex: -1,
		}
		if isRetainedProviderCredential(cred) {
			ref.normalizedIndex = normalizedIndex
			normalizedIndex++
		}
		refs = append(refs, ref)
	}
	return refs, seq, nil
}

func (d *authYAMLDocument) upsertAPIKeyCredential(provider, value string) (bool, error) {
	seq, err := d.providerSequence(provider, true)
	if err != nil {
		return false, err
	}
	for _, node := range seq.Content {
		if node.Kind == yaml.ScalarNode && node.Value == value {
			return false, nil
		}
	}
	seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	d.dirty = true
	return true, nil
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

func writeAuthYAMLFile(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("auth config path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create auth config dir: %w", err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create auth config tmp: %w", err)
	}
	tmpPath := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("set auth config tmp permissions: %w", err)
	}
	if n, err := f.Write(data); err != nil {
		return fmt.Errorf("write auth config tmp: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("write auth config tmp: %w", io.ErrShortWrite)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close auth config tmp: %w", err)
	}
	exists := false
	if _, err := os.Stat(path); err == nil {
		exists = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check auth config path %s: %w", path, err)
	}
	if exists {
		if err := os.Rename(tmpPath, path); err != nil {
			return fmt.Errorf("install auth config: %w", err)
		}
		return nil
	}
	if err := os.Link(tmpPath, path); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists; rerun chord to continue", path)
		}
		return fmt.Errorf("install auth config: %w", err)
	}
	return nil
}
func (d *authYAMLDocument) removeOAuthCredentials(remove func(provider string, cred *OAuthCredential, normalizedIndex int) bool) ([]RemovedOAuthCredentialEntry, error) {
	if d == nil {
		return nil, nil
	}
	root := d.rootMapping()
	var removed []RemovedOAuthCredentialEntry
	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode := root.Content[i]
		seq := root.Content[i+1]
		if keyNode == nil || keyNode.Kind != yaml.ScalarNode || seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		provider := keyNode.Value
		newContent := make([]*yaml.Node, 0, len(seq.Content))
		normalizedIndex := 0
		changed := false
		for _, node := range seq.Content {
			var cred ProviderCredential
			if err := node.Decode(&cred); err != nil {
				return nil, fmt.Errorf("decode auth provider %q credential: %w", provider, err)
			}
			if cred.OAuth != nil {
				idx := normalizedIndex
				if remove(provider, cred.OAuth, idx) {
					removed = append(removed, RemovedOAuthCredentialEntry{
						Provider:        provider,
						CredentialIndex: idx,
						AccountID:       cred.OAuth.AccountID,
						Email:           cred.OAuth.Email,
						Access:          cred.OAuth.Access,
						Status:          cred.OAuth.Status,
					})
					changed = true
					continue
				}
			}
			newContent = append(newContent, node)
			if isRetainedProviderCredential(cred) {
				normalizedIndex++
			}
		}
		if changed {
			seq.Content = newContent
			d.dirty = true
		}
	}
	return removed, nil
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
	var (
		accountIDMatch    *authCredentialNodeRef
		accountIndexMatch *authCredentialNodeRef
		accessMatch       *authCredentialNodeRef
		indexMatch        *authCredentialNodeRef
	)
	for i := range refs {
		if refs[i].credential.OAuth == nil {
			continue
		}
		oauth := refs[i].credential.OAuth
		if match.AccountID != "" && oauth.AccountID == match.AccountID {
			if match.Access != "" && oauth.Access == match.Access {
				return &refs[i]
			}
			if match.CredentialIndex != nil && refs[i].normalizedIndex == *match.CredentialIndex {
				accountIndexMatch = &refs[i]
				continue
			}
			if accountIDMatch == nil {
				accountIDMatch = &refs[i]
			}
			continue
		}
		if accessMatch == nil && match.Access != "" && oauth.Access == match.Access {
			accessMatch = &refs[i]
		}
		if indexMatch == nil && match.CredentialIndex != nil && refs[i].normalizedIndex == *match.CredentialIndex {
			indexMatch = &refs[i]
		}
	}
	if accountIndexMatch != nil {
		return accountIndexMatch
	}
	if accountIDMatch != nil {
		return accountIDMatch
	}
	if accessMatch != nil {
		return accessMatch
	}
	return indexMatch
}

func newOAuthCredentialNode(cred *OAuthCredential) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	updateOAuthMappingNode(node, cred)
	return node
}

func updateOAuthMappingNode(node *yaml.Node, cred *OAuthCredential) bool {
	changed := false
	changed = setMappingString(node, "refresh", cred.Refresh, true) || changed
	changed = setMappingString(node, "access", cred.Access, true) || changed
	changed = setMappingOptionalInt64(node, "expires", cred.Expires) || changed
	changed = setMappingString(node, "account_id", cred.AccountID, true) || changed
	changed = setMappingString(node, "email", cred.Email, true) || changed
	changed = removeMappingKey(node, "status") || changed
	changed = setMappingOptionalInt64(node, "codex_primary_reset_at", cred.CodexPrimaryResetAt) || changed
	changed = setMappingOptionalInt64(node, "codex_secondary_reset_at", cred.CodexSecondaryResetAt) || changed
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

func setMappingOptionalInt64(node *yaml.Node, key string, value int64) bool {
	if value == 0 {
		return removeMappingKey(node, key)
	}
	return setMappingInt64(node, key, value)
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
