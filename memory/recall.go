package memory

import (
	"context"
	"sort"
	"strings"
)

// SimpleRecaller is the default Recaller. It builds a Query from the
// conversation hint (current message text is the substring query) and
// asks the Store for matches, then re-scores by:
//
//  1. text overlap (how many query tokens appear in entry text)
//  2. tag presence (matched tags from conversation surface)
//  3. recency (newer wins ties)
//
// The strategy is intentionally cheap — no embeddings, no remote
// calls. Hosts that want semantic search plug their own Recaller
// without touching the Store.
type SimpleRecaller struct {
	// IncludeKinds, when non-empty, limits recall to entries whose
	// Kind appears in the list. Useful for excluding low-signal
	// "reference" entries when you only want preferences and
	// project facts.
	IncludeKinds []string

	// MinTokenLength filters short tokens out of the text-overlap
	// score. Default 3 — "is", "of", "and" don't help retrieval.
	MinTokenLength int

	// RequireMatch drops entries with no lexical signal (score 0:
	// no token or tag overlap with the hint). Off by default, so a
	// hint that tokenizes to nothing (e.g. "Oh hi!") otherwise
	// returns the most recent entries by recency — irrelevant
	// recall. Hosts wanting strict relevance enable it via
	// WithRequireMatch so the keyword path is gated like the
	// vector path's WithMinScore floor.
	RequireMatch bool

	// Stopwords are dropped from the hint before scoring. Without
	// them, common words ("the", "what", "did") produce spurious
	// substring matches that pull in unrelated memories. Empty by
	// default; set via WithStopwords (DefaultStopwords is a ready
	// English list).
	Stopwords map[string]struct{}
}

// SimpleRecallerOption configures a SimpleRecaller.
type SimpleRecallerOption func(*SimpleRecaller)

// WithRequireMatch makes the recaller drop entries with no token/tag
// overlap with the hint, instead of padding results by recency.
func WithRequireMatch() SimpleRecallerOption {
	return func(r *SimpleRecaller) { r.RequireMatch = true }
}

// WithStopwords drops the given words (lowercased) from the hint
// before scoring. Pass DefaultStopwords for a standard English set,
// optionally extended with domain terms.
func WithStopwords(words ...string) SimpleRecallerOption {
	return func(r *SimpleRecaller) {
		r.Stopwords = make(map[string]struct{}, len(words))
		for _, w := range words {
			r.Stopwords[strings.ToLower(w)] = struct{}{}
		}
	}
}

// DefaultStopwords is a standard English stopword list (3+ chars,
// since shorter tokens are already dropped by MinTokenLength). Common
// query glue that otherwise causes spurious keyword matches.
var DefaultStopwords = []string{
	"the", "and", "are", "was", "were", "been", "being", "for", "with",
	"from", "that", "this", "these", "those", "its", "did", "does", "done",
	"what", "which", "who", "whom", "whose", "when", "where", "why", "how",
	"can", "could", "will", "would", "should", "shall", "may", "might",
	"must", "have", "has", "had", "you", "your", "yours", "our", "ours",
	"they", "them", "their", "his", "her", "hers", "she", "him",
	"about", "into", "than", "then", "not", "yes", "get", "got", "just",
	"any", "all", "some", "such", "out", "off", "over", "more", "most",
	"there", "here", "also", "only", "very", "much", "many",
}

// NewSimpleRecaller returns a Recaller with conservative defaults
// (no Kind filter, MinTokenLength=3).
func NewSimpleRecaller(opts ...SimpleRecallerOption) *SimpleRecaller {
	r := &SimpleRecaller{MinTokenLength: 3}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Recall returns the top max entries for the agent. ConversationHint
// is parsed into tokens; tokens at or above MinTokenLength contribute
// to the substring query and to per-entry scoring.
func (r *SimpleRecaller) Recall(ctx context.Context, store Store, agentID string, conversationHint string, max int) ([]Entry, error) {
	if max <= 0 {
		return nil, nil
	}
	tokens := r.tokenize(conversationHint)
	// Store.Recall handles AgentID + Kind/Tag/exact filtering. Text
	// scoring belongs here — Stores that don't have semantic search
	// would lose entries when we filter on a single token that
	// happens to be absent from a relevant entry's text. Hosts with
	// an embedding-backed Store can override SimpleRecaller with
	// their own to push Text into the Store layer.
	candidates, err := store.Recall(ctx, Query{AgentID: agentID}, 0)
	if err != nil {
		return nil, err
	}
	if len(r.IncludeKinds) > 0 {
		want := make(map[string]struct{}, len(r.IncludeKinds))
		for _, k := range r.IncludeKinds {
			want[k] = struct{}{}
		}
		filtered := candidates[:0]
		for _, e := range candidates {
			if _, ok := want[e.Kind]; ok {
				filtered = append(filtered, e)
			}
		}
		candidates = filtered
	}

	type scored struct {
		entry Entry
		score int
	}
	out := make([]scored, 0, len(candidates))
	for _, e := range candidates {
		s := r.score(e, tokens)
		if r.RequireMatch && s == 0 {
			// No lexical signal — skip rather than pad by recency.
			continue
		}
		out = append(out, scored{entry: e, score: s})
	}
	sort.SliceStable(out, func(i, j int) bool {
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
	return final, nil
}

// score is the per-entry relevance number. Higher = more relevant.
// Text-overlap dominates (5 points/token); tag match adds 1.
func (r *SimpleRecaller) score(e Entry, tokens []string) int {
	text := strings.ToLower(e.Text)
	score := 0
	for _, tok := range tokens {
		if strings.Contains(text, tok) {
			score += 5
		}
		for _, tag := range e.Tags {
			if strings.ToLower(tag) == tok {
				score += 1
			}
		}
	}
	return score
}

// tokenize splits s into lowercase tokens that meet MinTokenLength.
// Punctuation and whitespace are separators; nothing fancier — for
// English-shaped agent prompts this is fine. Hosts that need real
// language analysis plug in a different Recaller.
func (r *SimpleRecaller) tokenize(s string) []string {
	min := r.MinTokenLength
	if min <= 0 {
		min = 3
	}
	out := make([]string, 0, 16)
	var cur []byte
	flush := func() {
		if len(cur) >= min {
			tok := string(cur)
			if _, stop := r.Stopwords[tok]; !stop {
				out = append(out, tok)
			}
		}
		cur = cur[:0]
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
			cur = append(cur, ch+('a'-'A'))
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			cur = append(cur, ch)
		default:
			flush()
		}
	}
	flush()
	return out
}

