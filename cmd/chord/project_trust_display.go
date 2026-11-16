package main

import (
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

func disclosureEndpoint(value string) string {
	if !strings.Contains(value, "://") {
		return truncateDisclosureTarget(value)
	}
	endpoint, err := url.Parse(value)
	if err != nil {
		return "(endpoint; contents hidden)"
	}
	endpoint.User = nil
	endpoint.RawQuery = ""
	endpoint.ForceQuery = false
	endpoint.Fragment = ""
	endpoint.RawFragment = ""
	return truncateDisclosureTarget(endpoint.String())
}

func redactedProjectConfig(data []byte) string {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return "(configuration could not be parsed; contents hidden)"
	}
	redactProjectNode(&node)
	out, err := yaml.Marshal(&node)
	if err != nil {
		return "(configuration could not be displayed)"
	}
	return string(out)
}

func redactProjectNode(node *yaml.Node) {
	node.HeadComment, node.LineComment, node.FootComment = "", "", ""
	node.Anchor = ""
	if node.Kind == yaml.AliasNode {
		*node = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "[hidden]"}
		return
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			redactProjectNode(key)
			name := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(key.Value))
			if strings.Contains(name, "key") || strings.Contains(name, "token") ||
				strings.Contains(name, "secret") || strings.Contains(name, "password") ||
				strings.Contains(name, "credential") || strings.Contains(name, "cookie") ||
				name == "authorization" || name == "env" || name == "headers" ||
				name == "command" || name == "commands" || name == "args" || name == "shell" {
				*value = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "[hidden]"}
			} else {
				redactProjectNode(value)
			}
		}
		return
	}
	for _, child := range node.Content {
		redactProjectNode(child)
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" && strings.Contains(node.Value, "://") {
		node.Value = disclosureEndpoint(node.Value)
	}
}

func projectTrustDisplayText(text string) string {
	text = terminaltext.Sanitize(text)
	return strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(text)
}
