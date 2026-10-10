package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type artifactViewCapability struct{}

func (artifactViewCapability) SupportsViewImageTool() bool { return true }

func imageAuthorizationFixture(t *testing.T) (string, string, string) {
	t.Helper()
	dir, cwd := t.TempDir(), t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	img, err := imagegen.ValidateImage(t.Context(), buf.Bytes(), "")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := tools.SaveImageArtifact(t.Context(), dir, img)
	if err != nil {
		t.Fatal(err)
	}
	return dir, cwd, tools.ImageArtifactPrefix + ref.RelPath
}

func TestImageArtifactPermissionPipeline(t *testing.T) {
	for _, agentID := range []string{"main", "worker-1"} {
		for _, name := range []string{tools.NameViewImage, tools.NameGenerateImage} {
			for _, action := range []string{"deny", "ask", "allow"} {
				t.Run(agentID+"/"+name+"/"+action, func(t *testing.T) {
					dir, cwd, ref := imageAuthorizationFixture(t)
					actual, err := tools.ResolveImageArtifactPath(dir, ref, cwd)
					if err != nil {
						t.Fatal(err)
					}
					pathTool := name
					args := map[string]any{"path": ref}
					if name == tools.NameGenerateImage {
						pathTool = tools.NameRead
						args = map[string]any{"prompt": "A tree", "operation": "edit", "reference_images": []string{ref}}
					}
					rules := permissionRuleset(t, fmt.Sprintf("\"*\": allow\n%s:\n  %q: %s\n", pathTool, filepath.ToSlash(actual), action))
					raw, _ := json.Marshal(args)
					confirms := 0
					pipe := toolExecutionPipeline{agentID: agentID, sessionDir: dir, toolBaseDir: cwd, currentRuleset: func() permission.Ruleset { return rules }, confirm: func(ctx context.Context, _, _ string, paths, _, _, _ []string) (ConfirmResponse, error) {
						confirms++
						if tools.AgentIDFromContext(ctx) != agentID || len(paths) != 1 || paths[0] != actual {
							t.Fatalf("confirmation has wrong binding: %v", paths)
						}
						return ConfirmResponse{Approved: true}, nil
					}}
					tc := message.ToolCall{ID: "image-call", Name: name, Args: raw}
					err = pipe.applyPermission(t.Context(), &tc, &ToolExecutionResult{})
					if action == "deny" {
						if err == nil {
							t.Fatal("artifact denial bypassed")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if (action == "ask") != (confirms == 1) {
						t.Fatalf("confirmations=%d", confirms)
					}
					if err := pipe.imageAccessGuard(pathTool, actual); err != nil {
						t.Fatal("approved path rejected", err)
					}
					// Approval remains on the original checkout when the agent changes cwd.
					if err := pipe.imageAccessGuard(pathTool, ref); err != nil {
						t.Fatal(err)
					}
					rules = permissionRuleset(t, fmt.Sprintf("\"*\": allow\n%s:\n  %q: ask\n  %q: deny\n", pathTool, filepath.ToSlash(actual), filepath.Join(cwd, "other.png")))
					if err := pipe.imageAccessGuard(pathTool, actual); err == nil {
						t.Fatal("new ask reused prior approval")
					}
					rules = permissionRuleset(t, fmt.Sprintf("\"*\": allow\n%s:\n  %q: deny\n", pathTool, filepath.ToSlash(actual)))
					if err := pipe.imageAccessGuard(pathTool, actual); err == nil {
						t.Fatal("revoked path still allowed")
					}
				})
			}
		}
	}
}

func TestViewImageArtifactExecuteUsesPermissionGuard(t *testing.T) {
	dir, cwd, ref := imageAuthorizationFixture(t)
	registry := tools.NewRegistry()
	tool := tools.NewViewImageTool(artifactViewCapability{})
	tool.BaseDir = cwd
	registry.Register(tool)
	rules := permissionRuleset(t, `"*": allow`)
	p := toolExecutionPipeline{agentID: "main", sessionDir: dir, toolBaseDir: cwd, registry: registry, currentRuleset: func() permission.Ruleset { return rules }}
	raw, _ := json.Marshal(map[string]string{"path": ref})
	result, err := p.execute(t.Context(), message.ToolCall{ID: "view-call", Name: tools.NameViewImage, Args: raw}, false)
	if err != nil || len(result.Images) != 1 {
		t.Fatal("pipeline did not carry approved artifact", err)
	}
}

func TestImageBackendRejectsUnapprovedArtifactAsk(t *testing.T) {
	dir, cwd, ref := imageAuthorizationFixture(t)
	actual, err := tools.ResolveImageArtifactPath(dir, ref, cwd)
	if err != nil {
		t.Fatal(err)
	}
	a := &MainAgent{cachedWorkDir: cwd}
	a.ruleset = permissionRuleset(t, fmt.Sprintf("\"*\": allow\nread:\n  %q: ask\n", filepath.ToSlash(actual)))
	backend := &imageGenerationBackend{agent: a}
	request := imagegen.Request{Prompt: "A tree", Operation: imagegen.Edit, ReferenceImages: []string{ref}}
	ctx := tools.WithSessionDir(t.Context(), dir)
	if err := backend.Check(ctx, request); err == nil {
		t.Fatal("unapproved ask accepted")
	}
	raw, _ := json.Marshal(request)
	pipe := toolExecutionPipeline{sessionDir: dir, toolBaseDir: cwd, currentRuleset: a.effectiveRuleset, confirm: func(context.Context, string, string, []string, []string, []string, []string) (ConfirmResponse, error) {
		return ConfirmResponse{Approved: true}, nil
	}}
	call := message.ToolCall{Name: tools.NameGenerateImage, Args: raw}
	if err := pipe.applyPermission(ctx, &call, &ToolExecutionResult{}); err != nil {
		t.Fatal(err)
	}
	// Switch live cwd after approval; reference resolution stays with the call.
	a.cachedWorkDir = t.TempDir()
	if err := backend.Check(tools.WithImageAccessGuard(ctx, pipe.imageAccessGuard), request); err != nil {
		t.Fatal("approved bound reference rejected", err)
	}
}

func TestImageArtifactRejectsSessionSymlinkAlias(t *testing.T) {
	dir, cwd, ref := imageAuthorizationFixture(t)
	path, err := tools.ResolveImageArtifactPath(dir, ref, cwd)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "images", "alias.png")
	if err := os.Symlink(path, alias); err != nil {
		t.Skip(err)
	}
	raw, _ := json.Marshal(map[string]string{"path": "artifact:images/alias.png"})
	decision := evaluateToolPermissionInDirWithContext(permissionRuleset(t, `"*": allow`), tools.NameViewImage, raw, permission.PathScope{Cwd: cwd}, toolPermissionContext{SessionDir: dir})
	if decision.Action != permission.ActionDeny {
		t.Fatal("session symlink alias authorized")
	}
}
