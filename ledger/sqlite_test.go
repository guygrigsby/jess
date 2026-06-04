package ledger

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestSQLiteCommitGetChain(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = db.Close() }()

	var _ DurableSink = db // must satisfy DurableSink

	req := Event{EventID: NewEventID(), RunID: "r1", Kind: KindRequest, Args: []byte(`"do it"`)}
	if err := db.Record(req); err != nil {
		t.Fatalf("Record req: %v", err)
	}
	act := Event{EventID: NewEventID(), RunID: "r1", CallID: "c1", Kind: KindAction, Tool: "delete_file",
		Args: []byte(`{"path":"/tmp/x"}`), Refs: []Ref{{Source: RefTool, ID: "req"}}}
	if err := db.CommitAction(act); err != nil {
		t.Fatalf("CommitAction: %v", err)
	}
	res := Event{EventID: NewEventID(), RunID: "r1", CallID: "c1", Kind: KindToolResult, Result: []byte(`"ok"`)}
	if err := db.Record(res); err != nil {
		t.Fatalf("Record res: %v", err)
	}

	got, err := db.Get(act.EventID)
	if err != nil || got.Tool != "delete_file" {
		t.Fatalf("Get: %+v err=%v", got, err)
	}
	chain, err := db.Chain("r1")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if chain.Request.Kind != KindRequest || len(chain.Actions) != 1 || chain.Actions[0].Result.CallID != "c1" {
		t.Fatalf("chain wrong: %+v", chain)
	}
}

func TestCommitActionRejectsNonSelfExplaining(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = db.Close() }()
	// missing Tool/Args/Refs => not self-explaining => must error, must NOT store.
	bad := Event{EventID: NewEventID(), RunID: "r1", CallID: "c1", Kind: KindAction}
	if err := db.CommitAction(bad); err == nil {
		t.Fatal("CommitAction must reject a non-self-explaining action")
	}
	if _, err := db.Get(bad.EventID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected event must not be stored; Get returned %v", err)
	}
	// wrong kind
	if err := db.CommitAction(Event{EventID: NewEventID(), RunID: "r1", Kind: KindToolResult}); err == nil {
		t.Fatal("CommitAction must require KindAction")
	}
}

func TestInsertRejectsDuplicateAndZeroID(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Record(Event{RunID: "r1", Kind: KindRunEnd}); err == nil {
		t.Fatal("zero EventID must be rejected")
	}
	e := Event{EventID: NewEventID(), RunID: "r1", Kind: KindRunEnd}
	if err := db.Record(e); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := db.Record(e); err == nil {
		t.Fatal("duplicate EventID must error (plain INSERT, not INSERT OR REPLACE)")
	}
}

func TestDurabilityAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.db")
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	act := Event{EventID: NewEventID(), RunID: "r1", CallID: "c1", Kind: KindAction,
		Tool: "delete_file", Args: []byte(`{"path":"/tmp/x"}`), Refs: []Ref{{Source: RefTool, ID: "req"}}}
	if err := db.CommitAction(act); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db2.Close() }()
	got, err := db2.Get(act.EventID)
	if err != nil || got.Tool != "delete_file" {
		t.Fatalf("event not durable across reopen: got=%+v err=%v", got, err)
	}
}

func TestSQLiteConcurrentCommit(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := Event{EventID: NewEventID(), RunID: "r1", CallID: NewEventID().String(), Kind: KindAction,
				Tool: "t", Args: []byte(`{}`), Refs: []Ref{{Source: RefTool, ID: "req"}}}
			if err := db.CommitAction(e); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent CommitAction failed (SQLITE_BUSY regression?): %v", err)
	}
}
