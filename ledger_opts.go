package jess

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/internal/core"
)

// WithLedger redirects the audit sink. Pass ledger.DiscardSink{} to turn audit
// off explicitly; it is never off silently.
func WithLedger(sink ledger.Sink) Option {
	return func(c *core.Config, _ *newState) { c.Audit = sink }
}

var (
	defaultSinkOnce sync.Once
	defaultSinkVal  ledger.Sink
)

// defaultLedger returns the process-wide default JSONL audit sink under the user
// cache dir. Using a singleton prevents two jess.New() calls without WithLedger
// from opening separate file descriptors and interleaving writes to the same
// file. Falls back to DiscardSink if the path cannot be opened.
func defaultLedger() ledger.Sink {
	defaultSinkOnce.Do(func() {
		dir, err := os.UserCacheDir()
		if err != nil {
			defaultSinkVal = ledger.DiscardSink{}
			return
		}
		d := filepath.Join(dir, "jess")
		_ = os.MkdirAll(d, 0o700)
		s, err := ledger.NewJSONLSink(filepath.Join(d, "ledger.jsonl"))
		if err != nil {
			defaultSinkVal = ledger.DiscardSink{}
			return
		}
		defaultSinkVal = s
	})
	return defaultSinkVal
}
