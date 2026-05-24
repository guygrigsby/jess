package memory

import (
	"context"
	"errors"
	"sort"
)

// VectorRecaller is a Recaller that embeds the conversation hint
// and asks a VectorStore for nearest neighbors. Falls back with an
// error when the Store doesn't implement VectorStore — recallers
// don't silently degrade to keyword scoring, that's HybridRecaller's
// job.
//
// Usually wrapped inside HybridRecaller; standalone use is fine when
// the host KNOWS the Store is vector-backed and wants pure semantic
// retrieval.
type VectorRecaller struct {
	// Embedder is optional. When nil, VectorRecaller calls
	// store.(VectorStore).Embedder() to use whatever the Store
	// was built with — usually the right thing, and saves the
	// host from threading the same Embedder through twice.
	//
	// Set explicitly only when you want to query with an embedder
	// distinct from the one the Store indexed against. That's
	// almost always a mistake (the vectors would be in different
	// spaces) — the field exists for advanced testing only.
	Embedder Embedder
}

// NewVectorRecaller returns a VectorRecaller that uses the Store's
// own Embedder for query vectors.
func NewVectorRecaller() *VectorRecaller {
	return &VectorRecaller{}
}

// Recall embeds conversationHint and asks the VectorStore for the
// top max nearest entries.
func (r *VectorRecaller) Recall(ctx context.Context, store Store, agentID, conversationHint string, max int) ([]Entry, error) {
	if max <= 0 {
		return nil, nil
	}
	vs, ok := store.(VectorStore)
	if !ok {
		return nil, errors.New("memory: VectorRecaller requires a Store that implements VectorStore")
	}
	emb := r.Embedder
	if emb == nil {
		emb = vs.Embedder()
		if emb == nil {
			return nil, errors.New("memory: VectorRecaller has no Embedder and the Store provides none")
		}
	}
	vec, err := emb.Embed(ctx, conversationHint)
	if err != nil {
		return nil, err
	}
	return vs.SearchVector(ctx, vec, max, Query{AgentID: agentID})
}

// HybridRecaller combines multiple Recallers via reciprocal rank
// fusion: each contributing Recaller ranks its candidates; the
// fused score for each entry is the sum of 1/(K+rank) across
// contributors, and the top max entries by fused score win.
//
// The canonical use is one VectorRecaller + one SimpleRecaller —
// vector retrieval covers semantic matches ("what was that pricing
// model?"), token overlap covers keyword-exact matches ("the
// FOO_BAR_BAZ flag"). Either alone misses cases the other catches.
//
// K=60 is the RRF constant from the original paper; small values
// favor top-ranked items, large values flatten the ranking. 60
// has been the field default for over a decade.
type HybridRecaller struct {
	Recallers []Recaller
	K         int
}

// NewHybridRecaller returns a Recaller that fuses the given
// underlying Recallers with K=60. Pass them in priority order
// only for clarity — order does not affect fusion.
func NewHybridRecaller(recallers ...Recaller) *HybridRecaller {
	return &HybridRecaller{Recallers: recallers, K: 60}
}

// Recall calls every contributing Recaller in sequence with an
// overfetched max (3x), then fuses results via RRF and returns
// the top max. Per-Recaller failures are logged into the error
// return path only when ALL contributors fail; partial failures
// degrade gracefully so a missing embedder doesn't kill the
// whole recall.
func (r *HybridRecaller) Recall(ctx context.Context, store Store, agentID, conversationHint string, max int) ([]Entry, error) {
	if max <= 0 {
		return nil, nil
	}
	if len(r.Recallers) == 0 {
		return nil, errors.New("memory: HybridRecaller has no contributing Recallers")
	}
	k := r.K
	if k <= 0 {
		k = 60
	}

	rankings := make([][]Entry, 0, len(r.Recallers))
	var lastErr error
	overfetch := max * 3
	for _, sub := range r.Recallers {
		out, err := sub.Recall(ctx, store, agentID, conversationHint, overfetch)
		if err != nil {
			lastErr = err
			continue
		}
		rankings = append(rankings, out)
	}
	if len(rankings) == 0 {
		// Every contributor failed — surface the last error so the
		// caller can see why instead of getting a silent empty
		// recall.
		return nil, lastErr
	}

	return rrf(rankings, k, max), nil
}

// rrf computes reciprocal rank fusion. For each entry that appears
// in any ranking, score = Σ 1/(k+rank). Returns the top max by
// score. Ties broken by recency (matches SimpleRecaller's tiebreak).
func rrf(rankings [][]Entry, k, max int) []Entry {
	type scored struct {
		entry Entry
		score float64
	}
	bestByID := map[string]scored{}
	for _, ranking := range rankings {
		for rank, e := range ranking {
			contribution := 1.0 / float64(k+rank)
			cur, ok := bestByID[e.ID]
			if !ok {
				cur = scored{entry: e}
			}
			cur.score += contribution
			bestByID[e.ID] = cur
		}
	}
	out := make([]scored, 0, len(bestByID))
	for _, s := range bestByID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].entry.CreatedAt.After(out[j].entry.CreatedAt)
	})
	if len(out) > max {
		out = out[:max]
	}
	final := make([]Entry, 0, len(out))
	for _, s := range out {
		final = append(final, s.entry)
	}
	return final
}
