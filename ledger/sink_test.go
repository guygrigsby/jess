package ledger

import "testing"

func TestDiscardSinkIsNotDurable(t *testing.T) {
	var s Sink = DiscardSink{}
	if _, ok := s.(DurableSink); ok {
		t.Fatal("DiscardSink must NOT be a DurableSink (auditing off denies actions)")
	}
}

func TestJSONLSinkIsNotDurable(t *testing.T) {
	var s Sink = &JSONLSink{}
	if _, ok := s.(DurableSink); ok {
		t.Fatal("JSONLSink is a mirror, not a durable ledger; must not be DurableSink")
	}
}
