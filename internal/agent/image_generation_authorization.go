package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type imagePermissionResource struct {
	tool, path string
}

// Every approval and execution check follows the same resource inventory.
func imageGenerationResources(r imagegen.Request) []imagePermissionResource {
	resources := make([]imagePermissionResource, 0, len(r.ReferenceImages)+2)
	resources = append(resources, imagePermissionResource{tools.NameGenerateImage, "*"})
	for _, path := range r.ReferenceImages {
		resources = append(resources, imagePermissionResource{tools.NameRead, path})
	}
	if r.OutputPath != "" {
		resources = append(resources, imagePermissionResource{tools.NameWrite, r.OutputPath})
	}
	return resources
}

// imagePermissionGuard binds approvals to canonical resources and the exact
// rules that passed confirmation. A changed ruleset cannot reuse an ask.
func (p toolExecutionPipeline) imagePermissionGuard(tc message.ToolCall, approved permission.Ruleset) tools.ImageAccessGuard {
	scope := p.effectivePathScope()
	approved = slices.Clone(approved)
	resources := make(map[string]bool)
	add := func(tool, path string) {
		resolved, err := tools.ResolveImageArtifactPath(p.sessionDir, path, scope.Cwd)
		if err == nil {
			resources[tool+"\x00"+resolved] = true
		}
	}
	if tc.Name == tools.NameViewImage {
		add(tc.Name, extractToolArgument(tc.Name, llm.UnwrapToolArgs(tc.Args)))
	} else {
		r, err := tools.DecodeImageGenerationRequest(llm.UnwrapToolArgs(tc.Args))
		if err == nil {
			for _, resource := range imageGenerationResources(r) {
				if resource.tool == tools.NameGenerateImage {
					resources[resource.tool+"\x00"+resource.path] = true
				} else {
					add(resource.tool, resource.path)
				}
			}
		}
	}
	return func(tool, path string) error {
		if tool != tools.NameGenerateImage {
			resolved, err := tools.ResolveImageArtifactPath(p.sessionDir, path, scope.Cwd)
			if err != nil {
				return err
			}
			path = resolved
		}
		if !resources[tool+"\x00"+path] {
			return fmt.Errorf("image resource changed after permission approval")
		}
		if p.bypassPermission != nil && p.bypassPermission(tc.Name) {
			return nil
		}
		rules := approved
		if p.currentRuleset != nil {
			rules = p.currentRuleset()
		}
		if len(rules) == 0 {
			return nil
		}
		action := rules.EvaluatePath(tool, path, scope)
		if tool == tools.NameGenerateImage {
			action = rules.Evaluate(tool, path)
		}
		if action == permission.ActionDeny {
			return wrapToolPermissionDenied(tc.Name)
		}
		if action == permission.ActionAsk && !slices.Equal(rules, approved) {
			return wrapToolRequiresConfirmation(tc.Name)
		}
		return nil
	}
}

func evaluateImageGenerationPermission(rules permission.Ruleset, raw json.RawMessage, scope permission.PathScope, sessionDir string) toolPermissionDecision {
	var items []permissionAggregateItem
	r, err := tools.DecodeImageGenerationRequest(raw)
	if err != nil {
		return toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}
	}
	appendPath := func(tool, path string) {
		resolved := path
		var err error
		if scope.Cwd != "" || strings.HasPrefix(path, tools.ImageArtifactPrefix) {
			resolved, err = tools.ResolveImageArtifactPath(sessionDir, path, scope.Cwd)
		}
		if err != nil {
			items = append(items, permissionAggregateItem{Argument: path, Action: permission.ActionDeny})
			return
		}
		path = resolved
		item := permissionAggregateItem{Argument: path, Action: rules.EvaluatePath(tool, path, scope)}
		if item.Action == permission.ActionAsk {
			item.AskList = []string{path}
		}
		if item.Action == permission.ActionAllow {
			item.AllowList = []string{path}
		}
		items = append(items, item)
	}
	for _, resource := range imageGenerationResources(r) {
		if resource.tool == tools.NameGenerateImage {
			items = append(items, permissionAggregateItem{Argument: resource.path, Action: rules.Evaluate(resource.tool, resource.path)})
		} else {
			appendPath(resource.tool, resource.path)
		}
	}
	return aggregatePermissionItems(items, permission.ActionAllow, "*")
}
