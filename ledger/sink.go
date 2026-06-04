package ledger

// Sink receives audit Events. Implementations must be safe for concurrent use.
type Sink interface {
	Record(Event) error
}

// DurableSink is a Sink that can durably commit an action record. CommitAction
// returns nil only when the event is persisted (e.g. written to SQLite). The
// gate/middleware require a DurableSink before allowing a non-safe tool: no
// durable record, no action. Plain Sinks (DiscardSink, JSONLSink) are
// observation-only.
type DurableSink interface {
	Sink
	CommitAction(Event) error
}

// DiscardSink drops every Event. Turning recording off is explicit (pass this to
// jess.WithLedger), never silent.
type DiscardSink struct{}

// Record satisfies Sink.
func (DiscardSink) Record(Event) error { return nil }
