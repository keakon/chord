package tools

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/lsp"
)

func TestEditPrewarmFileEligibility(t *testing.T) {
	for _, toolName := range []string{NameWrite, NameEdit, NameApplyPatch} {
		for _, tt := range []struct {
			name, path                           string
			links                                [][2]string
			allowed, nilAuthorize, cancelled     bool
			wantAuthorization, wantLaunchAttempt bool
		}{
			{name: "regular file", path: "real/sample.go", allowed: true, wantAuthorization: true, wantLaunchAttempt: true},
			{name: "new file", path: "new.go", allowed: true, wantAuthorization: true, wantLaunchAttempt: true},
			{name: "final symlink", path: "alias.go", links: [][2]string{{"alias.go", "real/sample.go"}}, allowed: true, wantAuthorization: true},
			{name: "symlink chain", path: "alias.go", links: [][2]string{{"alias.go", "second.go"}, {"second.go", "real/sample.go"}}, allowed: true, wantAuthorization: true},
			{name: "dangling symlink", path: "alias.go", links: [][2]string{{"alias.go", "missing.go"}}, allowed: true, wantAuthorization: true},
			{name: "directory symlink", path: "alias/sample.go", links: [][2]string{{"alias", "real"}}, allowed: true, wantAuthorization: true, wantLaunchAttempt: true},
			{name: "denied", path: "real/sample.go", wantAuthorization: true},
			{name: "nil authorization", path: "real/sample.go", nilAuthorize: true},
			{name: "cancelled", path: "real/sample.go", allowed: true, cancelled: true},
			{name: "non Go", path: "sample.txt", allowed: true},
		} {
			t.Run(toolName+"/"+tt.name, func(t *testing.T) {
				root := lspTestDirectory(t)
				if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "real", "sample.go"), []byte("package sample\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				for _, link := range tt.links {
					if err := os.Symlink(filepath.Join(root, link[1]), filepath.Join(root, link[0])); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				}
				synctest.Test(t, func(t *testing.T) {
					var authorizationCalls, launchAttempts atomic.Int32
					var manager *lsp.Manager
					manager = lsp.NewManager(&config.Config{LSP: config.LSPConfig{
						"sample-lsp": {Command: filepath.Join(root, "missing-language-server"), FileTypes: []string{".go"}},
					}}, root, func(eventType string, _ any) {
						if eventType != lsp.TypeLSPSidebarStatus {
							return
						}
						launchAttempts.Add(1)
						stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						manager.Stop(stopCtx)
					})
					defer func() {
						stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						manager.Stop(stopCtx)
					}()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if tt.cancelled {
						cancel()
					}
					var authorize func() bool
					if !tt.nilAuthorize {
						authorize = func() bool {
							authorizationCalls.Add(1)
							return tt.allowed
						}
					}
					switch toolName {
					case NameWrite:
						(WriteTool{LSP: manager, BaseDir: root}).PrewarmFile(ctx, tt.path, authorize)
					case NameEdit:
						(EditTool{LSP: manager, BaseDir: root}).PrewarmFile(ctx, tt.path, authorize)
					case NameApplyPatch:
						(ApplyPatchTool{LSP: manager, BaseDir: root}).PrewarmFile(ctx, tt.path, authorize)
					}
					// A missing executable makes every attempted launch report
					// failure and stop its watcher. Wait joins the background
					// work before absence of a launch is asserted.
					synctest.Wait()
					if got := authorizationCalls.Load(); got > 1 || (got == 1) != tt.wantAuthorization {
						t.Fatalf("authorization calls = %d, want called=%v", got, tt.wantAuthorization)
					}
					if got := launchAttempts.Load(); got > 1 || (got == 1) != tt.wantLaunchAttempt {
						t.Fatalf("launch attempts = %d, want attempted=%v", got, tt.wantLaunchAttempt)
					}
					if len(manager.TouchedPaths()) != 0 || len(manager.ReviewedPaths()) != 0 {
						t.Fatal("prewarm changed file review state")
					}
					if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
						t.Fatalf("prewarm created a file: %v", err)
					}
				})
			})
		}
	}
}
