package agent

import (
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestLooksLikeBuildLikeLogIncludesPatch(t *testing.T) {
	ctx := requestReductionContext{
		ToolName: tools.NamePatch,
		Content:  "Diagnostics:\nwarning: unused variable\n",
	}
	if !looksLikeBuildLikeLog(ctx) {
		t.Fatal("patch diagnostics output should be treated as build-like log")
	}
}
