package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	ev := Event{
		Time:      time.Unix(1, 0).UTC(),
		AgentPath: "root",
		Kind:      KindToolRequest,
		Tool:      "restart_service",
		Args:      json.RawMessage(`{"name":"nginx"}`),
	}
	if err := s.Record(ev); err != nil {
		t.Fatalf("Record: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got Event
	if err := json.Unmarshal(b[:len(b)-1], &got); err != nil { // strip trailing newline
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
