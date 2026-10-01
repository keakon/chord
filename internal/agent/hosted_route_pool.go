package agent

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
)

// hostedPoolSnapshot is the immutable named-pool routing source behind one or
// more hosted tools' model_pool setting. It is built once at startup from the
// final merged configuration and never follows main or subagent client pool
// switches, so a dedicated tool pool does not disappear when the conversation
// changes models. The generation is derived from the pool name and per-ref
// construction outcome: any rebuild with different content or order produces
// a new generation, which invalidates stale sticky routes.
type hostedPoolSnapshot struct {
	poolName             string
	refs                 []string            // resolved refs in configured order
	targets              []llm.FallbackModel // refs that constructed successfully, in the same relative order
	constructionFailures []hostedPoolConstructionFailure
	generation           uint64
}

type hostedPoolConstructionFailure struct {
	ref string
	err error
}

// buildHostedPoolSnapshot resolves poolName against the final merged
// configuration and constructs every ref in order. An unknown or empty pool is
// a startup configuration error; per-ref construction failures are logged here
// and only shrink the target list, so a snapshot whose refs all fail still
// exists and the tools routing to it stay hidden with an explicit reason
// rather than falling back to the caller's pool.
func (a *MainAgent) buildHostedPoolSnapshot(poolName string, modelPools map[string][]string) (*hostedPoolSnapshot, error) {
	refs, exists := modelPools[poolName]
	refs = trimModelPoolRefs(refs)
	if !exists {
		return nil, fmt.Errorf("model pool %q is not defined", poolName)
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("model pool %q is empty", poolName)
	}
	snap := &hostedPoolSnapshot{poolName: poolName, refs: refs}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(poolName))
	_, _ = hash.Write([]byte{0})
	for _, ref := range refs {
		entry, err := a.buildHostedPoolEntry(ref)
		if err != nil {
			log.Warnf("hosted model_pool %q: skipping unusable model ref error=%v", poolName, err)
			snap.constructionFailures = append(snap.constructionFailures, hostedPoolConstructionFailure{ref: ref, err: err})
			_, _ = hash.Write([]byte{0})
		} else {
			snap.targets = append(snap.targets, entry)
			_, _ = hash.Write([]byte{1})
		}
		_, _ = hash.Write([]byte(ref))
		_, _ = hash.Write([]byte{0})
	}
	snap.generation = hash.Sum64()
	return snap, nil
}

// buildHostedPoolEntry constructs one routing target for a model ref through
// the model switch factory. It deliberately does not go through
// newAuxModelPoolClient: that helper rebuilds clients with a shared total
// timeout and returns a pool-level client, while hosted routing needs raw
// per-target entries (each attempt builds its own single-target client) and
// must keep per-ref failures instead of returning a shrunk pool.
func (a *MainAgent) buildHostedPoolEntry(ref string) (llm.FallbackModel, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return llm.FallbackModel{}, fmt.Errorf("empty model ref")
	}
	if a.modelSwitchFactory == nil {
		return llm.FallbackModel{}, fmt.Errorf("model switch factory is not configured")
	}
	client, _, _, err := a.modelSwitchFactory(ref, nil, "")
	if err != nil {
		return llm.FallbackModel{}, fmt.Errorf("%s: %w", ref, err)
	}
	if client == nil {
		return llm.FallbackModel{}, fmt.Errorf("%s: produced nil client", ref)
	}
	defer client.Close()
	entry := client.PrimaryModelEntry()
	if entry.ProviderConfig == nil || entry.ProviderImpl == nil || strings.TrimSpace(entry.ModelID) == "" {
		return llm.FallbackModel{}, fmt.Errorf("%s: produced unusable client", ref)
	}
	return entry, nil
}
