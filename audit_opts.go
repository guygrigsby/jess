package jess

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/internal/core"
)

// WithAudit redirects the audit sink. Pass audit.DiscardSink{} to turn audit
// off explicitly; it is never off silently.
func WithAudit(sink audit.Sink) Option {
	return func(c *core.Config, _ *newState) { c.Audit = sink }
}

var (
	defaultSinkOnce sync.Once
	defaultSinkVal  audit.Sink
)

// defaultAudit returns the process-wide default JSONL audit sink under the user
// cache dir. Using a singleton prevents two jess.New() calls without WithAudit
// from opening separate file descriptors and interleaving writes to the same
// file. Falls back to DiscardSink if the path cannot be opened.
func defaultAudit() audit.Sink {
	defaultSinkOnce.Do(func() {
		dir, err := os.UserCacheDir()
		if err != nil {
			defaultSinkVal = audit.DiscardSink{}
			return
		}
		d := filepath.Join(dir, "jess")
		_ = os.MkdirAll(d, 0o700)
		s, err := audit.NewJSONLSink(filepath.Join(d, "audit.jsonl"))
		if err != nil {
			defaultSinkVal = audit.DiscardSink{}
			return
		}
		defaultSinkVal = s
	})
	return defaultSinkVal
}
