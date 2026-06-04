package ledger

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

// JSONLSink appends each Event as one JSON line to a file. Durable and
// crash-evident: a partial last line is the only possible corruption.
type JSONLSink struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

// NewJSONLSink opens (creating, append mode) the file at path.
func NewJSONLSink(path string) (*JSONLSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &JSONLSink{f: f, w: bufio.NewWriter(f)}, nil
}

// Record appends ev and flushes so the line is durable before returning.
func (s *JSONLSink) Record(ev Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(b); err != nil {
		return err
	}
	if err := s.w.WriteByte('\n'); err != nil {
		return err
	}
	return s.w.Flush()
}

// Close flushes and closes the underlying file.
func (s *JSONLSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.Flush(); err != nil {
		return err
	}
	return s.f.Close()
}
