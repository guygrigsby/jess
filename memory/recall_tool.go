package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// RecallTool is the tool the model uses to query the
// memory store on demand. The host's auto-recall pass already
// pulls relevant entries into the system prompt each turn, but
// "what did I tell you about X" style questions need a way for
// the model to explicitly look something up — without one, the
// model either fabricates or grep'es the workspace, neither of
// which is the right behavior.
//
// RecallTool wraps a Recaller (typically the same one the host's
// auto-recall uses) so semantic search semantics stay consistent
// between automatic and explicit recall.
type RecallTool struct {
	store    Store
	recaller Recaller
	agentID  string
	maxCap   int
}

// RecallTool's method set (Name, Description, Schema, Execute) satisfies any
// agent tool interface (e.g. agentcore.Tool) structurally; memory stays
// agentcore-free, so no compile-time assertion against that type lives here.

// RecallOptions configures NewRecallTool. AgentID is required —
// memories are agent-scoped and a recall without it returns
// nothing useful. MaxCap is the hard upper bound on results per
// call (default 8, mirrors what jess's auto-recall uses); the
// model can request fewer via the `max` arg but never more.
type RecallOptions struct {
	AgentID string
	MaxCap  int
}

// NewRecallTool builds the tool. Returns nil on impossible config
// (nil store or nil recaller) — callers should construct
// explicitly. Recaller is required because the tool's whole point
// is semantic search, not raw scan; pass NewHybridRecaller or
// NewVectorRecaller, not nil.
func NewRecallTool(store Store, recaller Recaller, opts RecallOptions) *RecallTool {
	if store == nil || recaller == nil {
		return nil
	}
	cap := opts.MaxCap
	if cap <= 0 {
		cap = 8
	}
	return &RecallTool{
		store:    store,
		recaller: recaller,
		agentID:  opts.AgentID,
		maxCap:   cap,
	}
}

// Name satisfies tool.Tool.
func (t *RecallTool) Name() string { return "recall" }

// Description is what the model sees. The phrasing is deliberate:
// the model needs to understand this is the ONLY way to query
// memory on demand (auto-recall handles the per-turn case), and
// that workspace grep is not a substitute.
func (t *RecallTool) Description() string {
	return "Search long-term memory for entries relevant to a query. " +
		"Use this when the user asks about something you might have remembered " +
		"in a prior turn or session (\"what did I tell you about X\", \"do you " +
		"know my preferences for Y\"). The auto-recall pass already injects the " +
		"most relevant entries into your context each turn, so call this only when " +
		"the auto-injected memories didn't surface what you need. Do NOT grep " +
		"workspace files as a substitute — memory lives in the vector store, not " +
		"in *.md files."
}

// Schema satisfies tool.Tool.
func (t *RecallTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Natural-language query. Semantic search; use the kind of phrasing the original memory would have used (e.g. \"food I ate today\" rather than \"diet\").",
			},
			"kind": map[string]any{
				"type":        "string",
				"description": "Optional filter: only return entries of this kind (user, feedback, project, reference). Omit to search all kinds.",
				"enum":        []string{"user", "feedback", "project", "reference"},
			},
			"max": map[string]any{
				"type":        "integer",
				"description": "Maximum entries to return. Capped server-side.",
				"minimum":     1,
			},
		},
		"required": []string{"query"},
	}
}

// recallArgs is the decoded JSON arg shape.
type recallArgs struct {
	Query string `json:"query"`
	Kind  string `json:"kind,omitempty"`
	Max   int    `json:"max,omitempty"`
}

// Execute satisfies tool.Tool. Decodes args, runs the
// recaller (or store.Recall when a kind filter is supplied —
// kind-scoped recall bypasses the semantic ranker so the model
// gets deterministic results when it asks for "all user-kind
// memories"). Returns a JSON list the model can quote from.
func (t *RecallTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args recallArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("recall: invalid args: %w", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return nil, fmt.Errorf("recall: query is required")
	}
	max := args.Max
	if max <= 0 || max > t.maxCap {
		max = t.maxCap
	}

	var entries []Entry
	var err error
	if args.Kind != "" {
		// Kind-scoped: scan rather than vector-search. The
		// recaller's ranker is tuned for unfiltered queries; when
		// the model asks for "all user-kind memories" it wants
		// every match, not the top-K by similarity.
		entries, err = t.store.Recall(ctx, Query{
			AgentID: t.agentID,
			Kind:    args.Kind,
		}, max)
	} else {
		entries, err = t.recaller.Recall(ctx, t.store, t.agentID, args.Query, max)
	}
	if err != nil {
		return nil, fmt.Errorf("recall: %w", err)
	}

	type respEntry struct {
		ID        string   `json:"id"`
		Kind      string   `json:"kind"`
		Text      string   `json:"text"`
		Key       string   `json:"key,omitempty"`
		Tags      []string `json:"tags,omitempty"`
		CreatedAt string   `json:"created_at"`
	}
	out := make([]respEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, respEntry{
			ID:        e.ID,
			Kind:      e.Kind,
			Text:      e.Text,
			Key:       e.Key,
			Tags:      e.Tags,
			CreatedAt: e.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	body, _ := json.Marshal(map[string]any{
		"query":   args.Query,
		"kind":    args.Kind,
		"count":   len(out),
		"entries": out,
	})
	return body, nil
}
