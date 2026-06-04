package jess

import (
	"os"
	"path/filepath"

	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/internal/core"
)

// WithAudit redirects the audit sink. Pass audit.DiscardSink{} to turn audit
// off explicitly; it is never off silently.
func WithAudit(sink audit.Sink) Option {
	return func(c *core.Config, _ *newState) { c.Audit = sink }
}

// defaultAudit opens a durable JSONL sink under the user cache dir. Falls back
// to Discard only if the path cannot be opened (audit must never block the run).
func defaultAudit() audit.Sink {
	dir, err := os.UserCacheDir()
	if err != nil {
		return audit.DiscardSink{}
	}
	d := filepath.Join(dir, "jess")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return audit.DiscardSink{}
	}
	s, err := audit.NewJSONLSink(filepath.Join(d, "audit.jsonl"))
	if err != nil {
		return audit.DiscardSink{}
	}
	return s
}
