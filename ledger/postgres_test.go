package ledger

import (
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
)

// openTestPostgres skips unless JESS_TEST_POSTGRES_DSN is set (make
// test-postgres provides it; plain `go test ./...` stays green without one).
func openTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("JESS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("JESS_TEST_POSTGRES_DSN not set; run `make test-postgres`")
	}
	pg, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

func TestPostgresCommitGetChain(t *testing.T) {
	pg := openTestPostgres(t)

	var _ DurableSink = pg // must satisfy DurableSink
	var _ Reader = pg      // must satisfy Reader

	runID := "r-" + NewEventID().String()
	req := Event{EventID: NewEventID(), RunID: runID, Kind: KindRequest, Args: []byte(`"do it"`)}
	if err := pg.Record(req); err != nil {
		t.Fatalf("Record req: %v", err)
	}
	act := Event{EventID: NewEventID(), RunID: runID, CallID: "c1", Kind: KindAction, Tool: "delete_file",
		Args: []byte(`{"path":"/tmp/x"}`), Refs: []Ref{{Source: RefTool, ID: "req"}}}
	if err := pg.CommitAction(act); err != nil {
		t.Fatalf("CommitAction: %v", err)
	}
	res := Event{EventID: NewEventID(), RunID: runID, CallID: "c1", Kind: KindToolResult, Result: []byte(`"ok"`)}
	if err := pg.Record(res); err != nil {
		t.Fatalf("Record res: %v", err)
	}

	got, err := pg.Get(act.EventID)
	if err != nil || got.Tool != "delete_file" {
		t.Fatalf("Get: %+v err=%v", got, err)
	}
	chain, err := pg.Chain(runID)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if chain.Request.Kind != KindRequest || len(chain.Actions) != 1 || chain.Actions[0].Result.CallID != "c1" {
		t.Fatalf("chain wrong: %+v", chain)
	}
}

func TestPostgresCommitActionRejectsNonSelfExplaining(t *testing.T) {
	pg := openTestPostgres(t)
	runID := "r-" + NewEventID().String()
	// missing Tool/Args/Refs => not self-explaining => must error, must NOT store.
	bad := Event{EventID: NewEventID(), RunID: runID, CallID: "c1", Kind: KindAction}
	if err := pg.CommitAction(bad); err == nil {
		t.Fatal("CommitAction must reject a non-self-explaining action")
	}
	if _, err := pg.Get(bad.EventID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected event must not be stored; Get returned %v", err)
	}
	// wrong kind
	if err := pg.CommitAction(Event{EventID: NewEventID(), RunID: runID, Kind: KindToolResult}); err == nil {
		t.Fatal("CommitAction must require KindAction")
	}
}

func TestPostgresInsertRejectsDuplicateAndZeroID(t *testing.T) {
	pg := openTestPostgres(t)
	runID := "r-" + NewEventID().String()
	if err := pg.Record(Event{RunID: runID, Kind: KindRunEnd}); err == nil {
		t.Fatal("zero EventID must be rejected")
	}
	e := Event{EventID: NewEventID(), RunID: runID, Kind: KindRunEnd}
	if err := pg.Record(e); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := pg.Record(e); err == nil {
		t.Fatal("duplicate EventID must error (plain INSERT, unique violation)")
	}
}

func TestPostgresDurabilityAcrossReconnect(t *testing.T) {
	pg := openTestPostgres(t)
	act := Event{EventID: NewEventID(), RunID: "r-" + NewEventID().String(), CallID: "c1", Kind: KindAction,
		Tool: "delete_file", Args: []byte(`{"path":"/tmp/x"}`), Refs: []Ref{{Source: RefTool, ID: "req"}}}
	if err := pg.CommitAction(act); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := pg.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	pg2, err := OpenPostgres(os.Getenv("JESS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer func() { _ = pg2.Close() }()
	got, err := pg2.Get(act.EventID)
	if err != nil || got.Tool != "delete_file" {
		t.Fatalf("event not durable across reconnect: got=%+v err=%v", got, err)
	}
}

func TestPostgresConcurrentCommit(t *testing.T) {
	pg := openTestPostgres(t)
	runID := "r-" + NewEventID().String()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := Event{EventID: NewEventID(), RunID: runID, CallID: NewEventID().String(), Kind: KindAction,
				Tool: "t", Args: []byte(`{}`), Refs: []Ref{{Source: RefTool, ID: "req"}}}
			if err := pg.CommitAction(e); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent CommitAction failed: %v", err)
	}
	chain, err := pg.Chain(runID)
	if err != nil || len(chain.Actions) != 50 {
		t.Fatalf("want 50 actions, got %d err=%v", len(chain.Actions), err)
	}
}
