package jess

import (
	"os"
	"path/filepath"

	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/internal/core"
)

// WithLedger redirects the audit sink. Pass ledger.DiscardSink{} to turn audit
// off explicitly; it is never off silently.
func WithLedger(sink ledger.Sink) Option {
	return func(c *core.Config, _ *newState) { c.Audit = sink }
}

// defaultLedger opens the durable SQLite audit ledger under the user cache dir.
// SQLite is a DurableSink, so audited non-safe actions can run by default: the
// audit middleware's "no durable record, no action" enforcement is satisfied.
// Falls back to DiscardSink if the cache dir or db cannot be opened, which is
// the safe failure (non-durable => non-safe actions are denied, never silently
// run unaudited).
func defaultLedger() ledger.Sink {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ledger.DiscardSink{}
	}
	d := filepath.Join(dir, "jess")
	_ = os.MkdirAll(d, 0o700)
	db, err := ledger.OpenSQLite(filepath.Join(d, "ledger.db"))
	if err != nil {
		return ledger.DiscardSink{}
	}
	return db
}
