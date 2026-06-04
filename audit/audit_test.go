package audit

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
