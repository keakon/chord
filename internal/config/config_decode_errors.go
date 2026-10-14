package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// typeErrorAt renders a wrong-typed value the way go-yaml's own decoder does
// ("line 4: cannot unmarshal !!map into ..."), so custom unmarshaler errors read
// the same as built-in ones and stripTypeInvalidOverride can still find the
// offending node from the message alone.
func typeErrorAt(node *yaml.Node, target string) *yaml.TypeError {
	tag := node.ShortTag()
	described := tag
	if tag != "!!map" && tag != "!!seq" {
		value := node.Value
		if len(value) > 10 {
			value = value[:7] + "..."
		}
		described = tag + " `" + value + "`"
	}
	return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: cannot unmarshal %s into %s", node.Line, described, target)}}
}

// unknownFieldErrorAt renders an unknown mapping key the way go-yaml reports
// one, pinned to the key's own line (see typeErrorAt).
func unknownFieldErrorAt(keyNode *yaml.Node, target string) string {
	return fmt.Sprintf("line %d: field %s not found in type %s", keyNode.Line, keyNode.Value, target)
}

// stripTypeInvalidOverride removes override leaves whose values cannot be
// decoded into the config schema (unknown fields, wrong types). Removing them
// before the map merge keeps every global value intact — an undecodable
// project value must fall back to the inherited value, never clobber it with a
// zero value. Returns the cleaned YAML bytes plus one structured path per removed
// leaf. Malformed YAML (syntax errors) is returned as a fatal error.
func stripTypeInvalidOverride(path string, data []byte) ([]byte, []configDroppedPath, error) {
	current := data
	var dropped []configDroppedPath
	for range maxOverrideSanitizeRounds {
		// Re-parse every round instead of carrying the first tree forward:
		// Marshal drops blank lines, so a node's Line only matches the lines
		// the decoder reports for the bytes it was parsed from.
		var root yaml.Node
		if err := yaml.Unmarshal(current, &root); err != nil {
			return nil, nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		if len(root.Content) == 0 {
			return current, dropped, nil
		}
		var probe Config
		terrors, derr := decodeStrict(current, &probe)
		if derr != nil {
			return nil, nil, fmt.Errorf("parse config %s: %w", path, derr)
		}
		if len(terrors) == 0 {
			return current, dropped, nil
		}
		removed := removeFailingNodes(root.Content[0], nil, parseTypeErrorFailures(terrors))
		if len(removed) == 0 {
			return nil, nil, fmt.Errorf("config %s: unable to isolate invalid values: %s", path, strings.Join(terrors, "; "))
		}
		dropped = append(dropped, removed...)
		next, err := yaml.Marshal(&root)
		if err != nil {
			return nil, nil, fmt.Errorf("rebuild sanitized config %s: %w", path, err)
		}
		current = next
	}
	return nil, nil, fmt.Errorf("config %s: too many invalid values to sanitize", path)
}

// yamlFailures locates the nodes a batch of go-yaml TypeErrors blames. The line
// number alone is not enough: go-yaml reports unknown or duplicate fields at the
// key node's line and type mismatches at the value node's line, and a block
// container's Line is its first child's line. Reading "line 3" as "the mapping
// starting on line 3" when it meant "the key on line 3" deletes that key's valid
// siblings, so the two sides are tracked apart, with the source tag recorded for
// type mismatches.
type yamlFailures struct {
	keys   map[int]bool            // "field X not found" / duplicate key: a key node
	values map[int]map[string]bool // "cannot unmarshal !!tag": a value node with that tag
	either map[int]bool            // unrecognized message shape: either side
}

// parseTypeErrorFailures classifies go-yaml TypeError messages by the node they
// point at. Messages without a "line N:" prefix carry no location and are
// skipped; custom unmarshalers in this package emit the same shapes so their
// errors stay locatable too (see HookCommand/ContextReductionConfig).
func parseTypeErrorFailures(terrors []string) yamlFailures {
	f := yamlFailures{
		keys:   make(map[int]bool),
		values: make(map[int]map[string]bool),
		either: make(map[int]bool),
	}
	for _, msg := range terrors {
		line, rest, ok := cutTypeErrorLine(msg)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(rest, "cannot unmarshal "):
			tag, _, _ := strings.Cut(strings.TrimPrefix(rest, "cannot unmarshal "), " ")
			if f.values[line] == nil {
				f.values[line] = make(map[string]bool)
			}
			f.values[line][tag] = true
		case strings.HasPrefix(rest, "field ") || strings.HasPrefix(rest, "mapping key "):
			f.keys[line] = true
		default:
			f.either[line] = true
		}
	}
	return f
}

// cutTypeErrorLine splits a "line N: rest" TypeError message.
func cutTypeErrorLine(msg string) (int, string, bool) {
	rest, ok := strings.CutPrefix(msg, "line ")
	if !ok {
		return 0, "", false
	}
	num, after, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, "", false
	}
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil {
		return 0, "", false
	}
	return n, strings.TrimSpace(after), true
}

func (f yamlFailures) empty() bool {
	return len(f.keys) == 0 && len(f.values) == 0 && len(f.either) == 0
}

// matchesKey reports whether a mapping key node is the blamed node.
func (f yamlFailures) matchesKey(n *yaml.Node) bool {
	return n != nil && (f.keys[n.Line] || f.either[n.Line])
}

// matchesValue reports whether a value node is the blamed node. A container
// claims the failure only when nothing below it can: go-yaml blames the deepest
// node, while every enclosing block container repeats that node's line.
func (f yamlFailures) matchesValue(n *yaml.Node) bool {
	return n != nil && f.ownsValueLine(n) && !f.descendantMatches(n)
}

func (f yamlFailures) ownsValueLine(n *yaml.Node) bool {
	if f.values[n.Line][n.ShortTag()] {
		return true
	}
	// An unclassified message carries no tag to check, so trust it only on a
	// node that cannot be standing in for a child sharing its line.
	return f.either[n.Line] && len(n.Content) == 0
}

// descendantMatches reports whether removeFailingNodes could pin the failure on
// something below n. It mirrors that function's rules exactly, which is what
// guarantees the recursion always finds a node to remove.
func (f yamlFailures) descendantMatches(n *yaml.Node) bool {
	if f.empty() {
		return false
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			keyNode, valNode := n.Content[i], n.Content[i+1]
			if f.matchesKey(keyNode) || f.ownsValueLine(valNode) || f.descendantMatches(valNode) {
				return true
			}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if f.ownsValueLine(item) || f.descendantMatches(item) {
				return true
			}
		}
	}
	return false
}

// removeFailingNodes prunes mapping pairs and sequence items the failures blame.
// The most specific node is removed first so sibling fields survive; a container
// is dropped whole only when the failure is about the container itself or its
// children could not absorb it. Returns path segments of everything removed.
func removeFailingNodes(node *yaml.Node, prefix []string, failures yamlFailures) []configDroppedPath {
	switch node.Kind {
	case yaml.MappingNode:
		kept := make([]*yaml.Node, 0, len(node.Content))
		var removed []configDroppedPath
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode, valNode := node.Content[i], node.Content[i+1]
			path := append(slices.Clip(prefix), keyNode.Value)
			if failures.matchesKey(keyNode) || failures.matchesValue(valNode) {
				removed = append(removed, configDroppedPath{Segments: slices.Clone(path)})
				continue
			}
			childRemoved := removeFailingNodes(valNode, path, failures)
			if failures.descendantMatches(valNode) {
				// Deeper nesting still failing after child cleanup: drop the
				// pair rather than loop forever. Its path supersedes the child
				// paths already collected under it.
				removed = append(removed, configDroppedPath{Segments: slices.Clone(path)})
				continue
			}
			removed = append(removed, childRemoved...)
			kept = append(kept, keyNode, valNode)
		}
		node.Content = kept
		return removed
	case yaml.SequenceNode:
		kept := make([]*yaml.Node, 0, len(node.Content))
		var removed []configDroppedPath
		for _, item := range node.Content {
			if failures.matchesValue(item) {
				removed = append(removed, configDroppedPath{Segments: slices.Clone(prefix)})
				continue
			}
			childRemoved := removeFailingNodes(item, prefix, failures)
			if failures.descendantMatches(item) {
				removed = append(removed, configDroppedPath{Segments: slices.Clone(prefix)})
				continue
			}
			removed = append(removed, childRemoved...)
			kept = append(kept, item)
		}
		node.Content = kept
		return removed
	default:
		return nil
	}
}
