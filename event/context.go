package event

import "context"

type streamKey struct{}

// ContextWithStream returns ctx carrying s as the active run's event stream.
// The anti-corruption layer injects the current run's stream so components
// running mid-run (such as a subagent tool) can forward events into it.
func ContextWithStream(ctx context.Context, s *Stream) context.Context {
	return context.WithValue(ctx, streamKey{}, s)
}

// StreamFromContext returns the active run's stream if one was injected and is
// non-nil.
func StreamFromContext(ctx context.Context) (*Stream, bool) {
	s, ok := ctx.Value(streamKey{}).(*Stream)
	return s, ok && s != nil
}
