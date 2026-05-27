package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// stubRecaller records its inputs and returns canned results so
// RecallTool.Execute can be exercised without a real ranker.
type stubRecaller struct {
	entries []Entry
	err     error

	calls   int
	gotMax  int
	gotHint string
}

func (r *stubRecaller) Recall(_ context.Context, _ Store, _ string, hint string, max int) ([]Entry, error) {
	r.calls++
	r.gotHint = hint
	r.gotMax = max
	return r.entries, r.err
}

func TestNewRecallTool_NilDeps(t *testing.T) {
	store := NewInMemoryStore()
	rec := &stubRecaller{}
	tests := []struct {
		name     string
		store    Store
		recaller Recaller
		wantNil  bool
	}{
		{"nil store", nil, rec, true},
		{"nil recaller", store, nil, true},
		{"both nil", nil, nil, true},
		{"both set", store, rec, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewRecallTool(tt.store, tt.recaller, RecallOptions{AgentID: "a"})
			if (got == nil) != tt.wantNil {
				t.Errorf("NewRecallTool nil=%v, want %v", got == nil, tt.wantNil)
			}
		})
	}
}

// recallResp mirrors the JSON shape Execute emits.
type recallResp struct {
	Query   string `json:"query"`
	Kind    string `json:"kind"`
	Count   int    `json:"count"`
	Entries []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"entries"`
}

func TestRecallTool_Execute(t *testing.T) {
	tests := []struct {
		name string
		// recaller behavior
		recallerEntries []Entry
		recallerErr     error
		// raw args
		args string
		// expectations
		wantErr       bool
		wantCount     int
		wantMaxPassed int  // expected max forwarded to recaller (0 = don't check)
		wantRecaller  bool // recaller should have been called
		wantFirstText string
	}{
		{
			name:    "invalid json",
			args:    `{"query":`,
			wantErr: true,
		},
		{
			name:    "empty query",
			args:    `{"query":"   "}`,
			wantErr: true,
		},
		{
			name:            "happy path forwards to recaller",
			recallerEntries: []Entry{{ID: "1", Kind: "project", Text: "ships friday"}},
			args:            `{"query":"release date"}`,
			wantCount:       1,
			wantMaxPassed:   8, // default cap when max omitted
			wantRecaller:    true,
			wantFirstText:   "ships friday",
		},
		{
			name:            "max clamps to cap",
			recallerEntries: []Entry{},
			args:            `{"query":"x","max":100}`,
			wantCount:       0,
			wantMaxPassed:   8,
			wantRecaller:    true,
		},
		{
			name:            "max below cap is honored",
			recallerEntries: []Entry{},
			args:            `{"query":"x","max":3}`,
			wantMaxPassed:   3,
			wantRecaller:    true,
		},
		{
			name:         "recaller error propagates",
			recallerErr:  errors.New("boom"),
			args:         `{"query":"x"}`,
			wantErr:      true,
			wantRecaller: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &stubRecaller{entries: tt.recallerEntries, err: tt.recallerErr}
			tool := NewRecallTool(NewInMemoryStore(), rec, RecallOptions{AgentID: "a"})

			body, err := tool.Execute(context.Background(), json.RawMessage(tt.args))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if rec.calls > 0 != tt.wantRecaller {
				t.Errorf("recaller called=%v, want %v", rec.calls > 0, tt.wantRecaller)
			}
			if tt.wantMaxPassed != 0 && rec.gotMax != tt.wantMaxPassed {
				t.Errorf("max forwarded = %d, want %d", rec.gotMax, tt.wantMaxPassed)
			}
			var resp recallResp
			if err := json.Unmarshal(body, &resp); err != nil {
				t.Fatalf("response not valid JSON: %v", err)
			}
			if resp.Count != tt.wantCount {
				t.Errorf("count = %d, want %d", resp.Count, tt.wantCount)
			}
			if tt.wantFirstText != "" {
				if len(resp.Entries) == 0 || resp.Entries[0].Text != tt.wantFirstText {
					t.Errorf("first entry text = %v, want %q", resp.Entries, tt.wantFirstText)
				}
			}
		})
	}
}

// A kind filter bypasses the recaller and scans the store directly, so
// the model gets every match of that kind rather than a top-K ranking.
func TestRecallTool_Execute_KindFilterBypassesRecaller(t *testing.T) {
	store := NewInMemoryStore()
	ctx := context.Background()
	_, _ = store.Append(ctx, Entry{Text: "prefers tabs", AgentID: "a", Kind: string(KindUser)})
	_, _ = store.Append(ctx, Entry{Text: "ships friday", AgentID: "a", Kind: string(KindProject)})

	// Recaller errors if used — proves the kind path doesn't touch it.
	rec := &stubRecaller{err: errors.New("recaller must not be called")}
	tool := NewRecallTool(store, rec, RecallOptions{AgentID: "a"})

	body, err := tool.Execute(ctx, json.RawMessage(`{"query":"anything","kind":"user"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.calls != 0 {
		t.Errorf("recaller was called %d times; kind filter must bypass it", rec.calls)
	}
	var resp recallResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || resp.Entries[0].Text != "prefers tabs" {
		t.Errorf("kind=user recall = %+v, want the single user entry", resp.Entries)
	}
}
