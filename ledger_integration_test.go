package jess_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/gate"
	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/memory"
)

// capLedger wraps SQLite and records the run id of committed actions so tests
// can look up Chain(runID) after the run without inspecting agent internals.
type capLedger struct {
	*ledger.SQLite
	mu    sync.Mutex
	runID string
}

func (c *capLedger) Record(e ledger.Event) error {
	c.mu.Lock()
	if e.RunID != "" {
		c.runID = e.RunID
	}
	c.mu.Unlock()
	return c.SQLite.Record(e)
}

func (c *capLedger) CommitAction(e ledger.Event) error {
	c.mu.Lock()
	if e.RunID != "" {
		c.runID = e.RunID
	}
	c.mu.Unlock()
	return c.SQLite.CommitAction(e)
}

func (c *capLedger) capturedRunID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runID
}

func newCapLedger(t *testing.T) *capLedger {
	t.Helper()
	db, err := ledger.OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	return &capLedger{SQLite: db}
}

// textHash returns the sha256 hex of a plain string, matching how a caller
// would compute the hash of an Entry's Text for a Ref.
func textHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// --- Test 1: end-to-end through jess.Stream ---

// TestChainReconstructsTriad proves that a run through jess.Stream produces a
// ledger chain with a KindRequest head, at least one Action whose Intent names
// the called tool with non-empty Args, and at least one Ref embedded in the
// Action's Evidence (the embedded "why").
func TestChainReconstructsTriad(t *testing.T) {
	store := memory.NewInMemoryStore()
	_, err := store.Append(context.Background(), memory.Entry{
		Kind: string(memory.KindUser),
		Text: "user prefers nginx restarts at 3am",
	})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	cap := newCapLedger(t)
	defer func() { _ = cap.SQLite.Close() }()

	approver := gate.Approver(func(_ context.Context, _ gate.Request) (bool, string) {
		return true, "approved in test"
	})

	rt := &restartTool{}
	agent := jess.New(
		jess.WithModel(callOnceModel("restart_service")),
		jess.WithTools(rt),
		jess.WithMemory(store, memory.NewSimpleRecaller()),
		jess.WithApprover(approver),
		jess.WithLedger(cap),
	)

	ch, wait := jess.Stream(context.Background(), agent, "restart nginx now")
	for range ch {
	}
	_ = wait()

	runID := cap.capturedRunID()
	if runID == "" {
		t.Fatal("no run id captured; ledger may not have received any events")
	}

	chain, err := cap.SQLite.Chain(runID)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}

	// The chain head must be the run request.
	if chain.Request.Kind != ledger.KindRequest {
		t.Fatalf("chain.Request.Kind = %q, want %q", chain.Request.Kind, ledger.KindRequest)
	}

	// The tool must have run (approver allowed it and cap is durable).
	if !rt.ran {
		t.Fatal("restart_service did not run; gate or durable-sink check failed")
	}

	// There must be at least one Action.
	if len(chain.Actions) == 0 {
		t.Fatal("chain has no Actions; KindAction event not recorded")
	}

	// The action's Intent must name the tool with non-empty Args.
	a := chain.Actions[0]
	if a.Intent.Tool != "restart_service" {
		t.Errorf("action Intent.Tool = %q, want %q", a.Intent.Tool, "restart_service")
	}
	if len(a.Intent.Args) == 0 {
		t.Error("action Intent.Args is empty")
	}

	// The Result must be present (tool_result paired by CallID).
	if a.Result.Kind != ledger.KindToolResult {
		t.Errorf("action Result.Kind = %q, want %q", a.Result.Kind, ledger.KindToolResult)
	}

	// Evidence must be non-empty: the action embeds the request ref.
	if len(a.Evidence) == 0 {
		t.Error("action Evidence is empty; no embedded 'why'")
	}

	// Available may be empty if SimpleRecaller found no overlap with the hint.
	// That is acceptable; the spec says focus on Request+Action+Evidence.
	t.Logf("chain.Available len=%d (may be 0 — SimpleRecaller lexical match not guaranteed)", len(chain.Available))
}

// --- Test 2: store-direct ---

// TestWhySurvivesLostHead proves that the "why" (Intent.Tool, Intent.Args,
// Intent.Reason) is recoverable from the action record alone even when the
// KindRequest head is absent. The chain degrades gracefully rather than
// silently dropping the action.
func TestWhySurvivesLostHead(t *testing.T) {
	db, err := ledger.OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer func() { _ = db.Close() }()

	runID := ledger.NewEventID().String()
	callID := "c-why-1"

	// Build a self-explaining KindAction event but intentionally omit the
	// KindRequest head.
	reqRef := ledger.Ref{
		Source: ledger.RefTool,
		ID:     ledger.NewEventID().String(),
		Hash:   "hypothetical-req-hash",
	}
	action := ledger.Event{
		EventID: ledger.NewEventID(),
		RunID:   runID,
		CallID:  callID,
		Time:    time.Now(),
		Kind:    ledger.KindAction,
		Tool:    "restart_service",
		Args:    json.RawMessage(`{"service":"nginx"}`),
		Verdict: ledger.VerdictAllowed,
		Reason:  "the why",
		Refs:    []ledger.Ref{reqRef},
	}
	if err := db.CommitAction(action); err != nil {
		t.Fatalf("CommitAction: %v", err)
	}

	chain, err := db.Chain(runID)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}

	// No head recorded => chain.Request is the zero Event; that is intentional.
	// The important claim: the action's why is still in the chain.
	if len(chain.Actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(chain.Actions))
	}
	a := chain.Actions[0]

	if a.Intent.Tool != "restart_service" {
		t.Errorf("Intent.Tool = %q, want \"restart_service\"", a.Intent.Tool)
	}
	if len(a.Intent.Args) == 0 {
		t.Error("Intent.Args is empty; can't reconstruct target")
	}
	if a.Intent.Reason != "the why" {
		t.Errorf("Intent.Reason = %q, want \"the why\"", a.Intent.Reason)
	}
}

// --- Test 3: store-direct ---

// TestCallIDPairingTwoCalls exercises the CallID pairing path directly. Two
// KindAction events with distinct CallIDs are each paired with their
// KindToolResult; the assembled chain must contain two distinct Actions.
func TestCallIDPairingTwoCalls(t *testing.T) {
	db, err := ledger.OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer func() { _ = db.Close() }()

	runID := ledger.NewEventID().String()

	// Record the KindRequest head.
	req := ledger.Event{
		EventID: ledger.NewEventID(),
		RunID:   runID,
		Time:    time.Now(),
		Kind:    ledger.KindRequest,
		Args:    json.RawMessage(`"restart both services"`),
	}
	if err := db.Record(req); err != nil {
		t.Fatalf("Record KindRequest: %v", err)
	}

	reqRef := ledger.Ref{Source: ledger.RefTool, ID: req.EventID.String()}

	// Two actions with distinct CallIDs.
	for _, callID := range []string{"c1", "c2"} {
		action := ledger.Event{
			EventID: ledger.NewEventID(),
			RunID:   runID,
			CallID:  callID,
			Time:    time.Now(),
			Kind:    ledger.KindAction,
			Tool:    "restart_service",
			Args:    json.RawMessage(`{"service":"nginx"}`),
			Verdict: ledger.VerdictAllowed,
			Reason:  "restart requested",
			Refs:    []ledger.Ref{reqRef},
		}
		if err := db.CommitAction(action); err != nil {
			t.Fatalf("CommitAction %s: %v", callID, err)
		}
	}

	// Two tool results with the same CallIDs.
	for _, callID := range []string{"c1", "c2"} {
		result := ledger.Event{
			EventID: ledger.NewEventID(),
			RunID:   runID,
			CallID:  callID,
			Time:    time.Now(),
			Kind:    ledger.KindToolResult,
			Tool:    "restart_service",
			Result:  json.RawMessage(`"restarted"`),
		}
		if err := db.Record(result); err != nil {
			t.Fatalf("Record KindToolResult %s: %v", callID, err)
		}
	}

	chain, err := db.Chain(runID)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}

	if len(chain.Actions) != 2 {
		t.Fatalf("want 2 actions, got %d", len(chain.Actions))
	}

	// Each action's Intent.CallID must match its Result.CallID.
	for i, a := range chain.Actions {
		if a.Intent.CallID == "" {
			t.Errorf("action[%d] Intent.CallID is empty", i)
		}
		if a.Result.CallID == "" {
			t.Errorf("action[%d] Result.CallID is empty", i)
		}
		if a.Intent.CallID != a.Result.CallID {
			t.Errorf("action[%d] Intent.CallID %q != Result.CallID %q", i, a.Intent.CallID, a.Result.CallID)
		}
	}

	// The two actions must have distinct CallIDs.
	if chain.Actions[0].Intent.CallID == chain.Actions[1].Intent.CallID {
		t.Errorf("both actions share CallID %q; pairing collapsed two calls into one", chain.Actions[0].Intent.CallID)
	}
}

// --- Test 4: store-direct ---

// TestMemoryRefDriftDetected proves that a Ref capturing a content hash at
// decision time allows drift detection later: appending a new entry under the
// same Key supersedes the old ID, so the previously-captured ID either
// disappears (Get returns false) or points to different content. In either
// case the stored hash no longer matches, proving drift is detectable without
// any special machinery beyond sha256(text).
func TestMemoryRefDriftDetected(t *testing.T) {
	store := memory.NewInMemoryStore()
	ctx := context.Background()

	// Append the original entry with a Key so it can be superseded.
	const key = "pref-restart-time"
	orig, err := store.Append(ctx, memory.Entry{
		Kind: string(memory.KindUser),
		Text: "user prefers restarts at 3am",
		Key:  key,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Capture a Ref at decision time: hash of the entry text as seen now.
	h1 := textHash(orig.Text)
	ref := ledger.Ref{
		Source: ledger.RefMemory,
		ID:     orig.ID,
		Hash:   h1,
	}

	// Supersede the entry by appending different text under the same Key.
	_, err = store.Append(ctx, memory.Entry{
		Kind: string(memory.KindUser),
		Text: "user now prefers restarts at midnight",
		Key:  key,
	})
	if err != nil {
		t.Fatalf("Append superseding entry: %v", err)
	}

	// After supersession, either the original ID is gone (deleted from the
	// store) or it points to the new content — both indicate drift.
	got, ok := store.Get(orig.ID)
	if !ok {
		// Original ID was replaced; drift is detectable because Get returned false.
		t.Logf("drift detected: entry %s no longer exists after supersession (ref.ID points to nothing)", ref.ID)
		return
	}

	// If the ID somehow survived, the text must have changed.
	currentHash := textHash(got.Text)
	if currentHash == h1 {
		t.Errorf("drift NOT detectable: sha256(%q) == sha256(%q) == %s; expected the content to have changed after supersession", got.Text, orig.Text, h1)
	} else {
		t.Logf("drift detected: hash at decision time %s != current hash %s", h1, currentHash)
	}
}
