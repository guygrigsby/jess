package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
)

type durRec struct {
	recSink
	commitErr error
	committed []ledger.Event
}

func (d *durRec) CommitAction(e ledger.Event) error {
	if d.commitErr != nil {
		return d.commitErr
	}
	d.committed = append(d.committed, e)
	return nil
}

func runMW(sink ledger.Sink, safe map[string]bool, rs *runState, name string) (json.RawMessage, error) {
	mw := auditMiddleware(sink, safe, rs, "root")
	call := ac.ToolCall{ID: "c1", Name: name, Args: []byte(`{"path":"/tmp/x"}`)}
	return mw(context.Background(), call, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return []byte(`"ran"`), nil
	})
}

func TestMWNonSafeDeniedWhenSinkNotDurable(t *testing.T) {
	rs := &runState{}
	_ = rs.begin("r1", ledger.NewEventID(), "do it")
	// safe map is empty: delete_file is not safe → enforced
	_, err := runMW(&recSink{}, map[string]bool{}, rs, "delete_file")
	if err == nil {
		t.Fatal("non-safe + non-durable sink must error (tool denied, never ran)")
	}
}

func TestMWNonSafeDeniedWhenCommitFails(t *testing.T) {
	rs := &runState{}
	_ = rs.begin("r1", ledger.NewEventID(), "do it")
	d := &durRec{commitErr: errors.New("disk full")}
	// safe map is empty: delete_file is not safe → enforced
	_, err := runMW(d, map[string]bool{}, rs, "delete_file")
	if err == nil {
		t.Fatal("commit failure must deny the action")
	}
}

func TestMWNonSafeCommitsThenRuns(t *testing.T) {
	rs := &runState{}
	_ = rs.begin("r1", ledger.NewEventID(), "do it")
	d := &durRec{}
	// safe map is empty: delete_file is not safe → enforced
	out, err := runMW(d, map[string]bool{}, rs, "delete_file")
	if err != nil || string(out) != `"ran"` {
		t.Fatalf("durable commit should allow the run: out=%s err=%v", out, err)
	}
	if len(d.committed) != 1 || d.committed[0].Kind != ledger.KindAction || d.committed[0].CallID != "c1" {
		t.Fatalf("expected one committed KindAction: %+v", d.committed)
	}
	if len(d.committed[0].Args) == 0 || len(d.committed[0].Refs) == 0 {
		t.Fatal("committed action must be self-explaining (Args + embedded why)")
	}
	if d.committed[0].RunID == "" {
		t.Fatal("committed action must have a RunID")
	}
}

func TestMWSafeToolBestEffort(t *testing.T) {
	rs := &runState{}
	_ = rs.begin("r1", ledger.NewEventID(), "look")
	// list is explicitly in the safe map → best-effort only
	out, err := runMW(&recSink{}, map[string]bool{"list": true}, rs, "list")
	if err != nil || string(out) != `"ran"` {
		t.Fatalf("safe tool must run best-effort: out=%s err=%v", out, err)
	}
}
