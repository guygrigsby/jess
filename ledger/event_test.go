package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONLSinkRecordsAndReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	s, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}

	ev1 := Event{Time: time.Unix(1, 0).UTC(), AgentPath: "root", Kind: KindToolRequest, Tool: "restart_service", Args: json.RawMessage(`{"name":"nginx"}`)}
	ev2 := Event{Time: time.Unix(2, 0).UTC(), Kind: KindToolResult, Tool: "restart_service"}
	if err := s.Record(ev1); err != nil {
		t.Fatalf("Record ev1: %v", err)
	}
	if err := s.Record(ev2); err != nil {
		t.Fatalf("Record ev2: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), string(b))
	}
	var got Event
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Tool != "restart_service" || got.Kind != KindToolRequest {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestDiscardSinkIsNoOp(t *testing.T) {
	if err := (DiscardSink{}).Record(Event{Kind: KindRunEnd}); err != nil {
		t.Fatalf("DiscardSink.Record: %v", err)
	}
}

func TestJSONLSinkConcurrentRecord(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLSink(filepath.Join(dir, "c.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Record(Event{Kind: KindToolRequest, Tool: "t"}); err != nil {
				t.Errorf("Record: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestNewEventIDMonotonicAndParsable(t *testing.T) {
	a := NewEventID()
	b := NewEventID()
	if a.Compare(b) >= 0 {
		t.Fatalf("ids must increase monotonically: %s !< %s", a, b)
	}
	if len(a.String()) != 26 {
		t.Fatalf("ulid string should be 26 chars, got %q", a.String())
	}
	// 1000 in a tight loop must stay strictly increasing (same-ms stress).
	prev := NewEventID()
	for i := 0; i < 1000; i++ {
		cur := NewEventID()
		if cur.Compare(prev) <= 0 {
			t.Fatalf("non-monotonic at %d: %s <= %s", i, cur, prev)
		}
		prev = cur
	}
}

func TestRefSourceValues(t *testing.T) {
	if RefTool == RefMemory {
		t.Fatal("ref sources must be distinct")
	}
	r := Ref{Source: RefMemory, ID: "m1", Hash: "abc"}
	if r.Source != RefMemory || r.ID != "m1" {
		t.Fatalf("ref fields: %+v", r)
	}
}
