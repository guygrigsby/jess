package subagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/internal/acl"
)

// Errors returned by Submit.
var (
	ErrUnknownAgent = errors.New("subagent: unknown agent")
	ErrPoolClosed   = errors.New("subagent: pool is closed")
	ErrMaxDepth     = errors.New("subagent: max depth exceeded")
)

// Default pool bounds.
const (
	defaultMaxConcurrent = 8
	defaultMaxQueued     = 1024
	defaultMaxDepth      = 8
	mergedStreamBuffer   = 256
)

// Pool runs subagent tasks with bounded concurrency and merges their events.
//
// Callers must call Close (graceful) or Cancel (abort) when done with the Pool;
// otherwise its worker goroutines run forever waiting for tasks.
type Pool struct {
	mu       sync.RWMutex
	specs    map[string]Spec
	maxDepth int

	// sendMu guards sends to / closing of tasks. Submit holds it for reading
	// across its channel send; Close holds it for writing while it closes the
	// channel, so a send can never race the close (no send-on-closed panic).
	sendMu sync.RWMutex
	closed bool

	tasks  chan *job
	stream *event.Stream
	g      *errgroup.Group
	ctx    context.Context
	cancel context.CancelFunc

	instance  atomic.Uint64
	closeOnce sync.Once
}

type job struct {
	spec  Spec
	input string
	path  []string
	sink  *event.Stream // nil => the pool's merged stream
	task  *Task
}

// Option configures a Pool.
type Option func(*poolConfig)

type poolConfig struct{ maxConcurrent, maxQueued, maxDepth int }

// WithMaxConcurrent caps how many subagent runs execute at once (default 8).
func WithMaxConcurrent(n int) Option { return func(c *poolConfig) { c.maxConcurrent = n } }

// WithMaxQueued caps how many tasks may wait in the queue; Submit blocks when
// full (default 1024).
func WithMaxQueued(n int) Option { return func(c *poolConfig) { c.maxQueued = n } }

// WithMaxDepth caps subagent nesting depth (default 8).
func WithMaxDepth(n int) Option { return func(c *poolConfig) { c.maxDepth = n } }

// New creates a Pool and starts its workers.
func New(opts ...Option) *Pool {
	cfg := poolConfig{maxConcurrent: defaultMaxConcurrent, maxQueued: defaultMaxQueued, maxDepth: defaultMaxDepth}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.maxConcurrent < 1 {
		cfg.maxConcurrent = 1
	}
	if cfg.maxQueued < 1 {
		cfg.maxQueued = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(ctx)
	p := &Pool{
		specs:    make(map[string]Spec),
		maxDepth: cfg.maxDepth,
		tasks:    make(chan *job, cfg.maxQueued),
		stream:   event.NewStream(mergedStreamBuffer),
		g:        g,
		ctx:      gctx,
		cancel:   cancel,
	}
	for i := 0; i < cfg.maxConcurrent; i++ {
		p.g.Go(func() error {
			for j := range p.tasks {
				p.runJob(j)
			}
			return nil
		})
	}
	go func() {
		_ = p.g.Wait()
		p.stream.Close()
	}()
	return p
}

// Register adds or replaces a subagent Spec.
func (p *Pool) Register(s Spec) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.specs[s.Name] = s
}

// Submit queues a run whose events go to the pool's merged stream (Events()).
// It blocks if the queue is full and returns when a slot is available or ctx
// is cancelled. parentPath is the caller's AgentPath (nil at top level).
func (p *Pool) Submit(ctx context.Context, name, input string, parentPath ...string) (*Task, error) {
	return p.submit(ctx, nil, name, input, parentPath...)
}

// SubmitTo queues a run whose events are forwarded to sink (AgentPath-tagged)
// instead of the pool's merged stream. Used to bubble a subagent's events into
// a parent run's stream. The sink is caller-owned; the pool never closes it.
func (p *Pool) SubmitTo(ctx context.Context, sink *event.Stream, name, input string, parentPath ...string) (*Task, error) {
	return p.submit(ctx, sink, name, input, parentPath...)
}

// submit is the shared implementation for Submit and SubmitTo.
func (p *Pool) submit(ctx context.Context, sink *event.Stream, name, input string, parentPath ...string) (*Task, error) {
	p.mu.RLock()
	spec, ok := p.specs[name]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
	}
	if len(parentPath) >= p.maxDepth {
		return nil, ErrMaxDepth
	}
	id := p.instance.Add(1)
	path := append(append([]string(nil), parentPath...), fmt.Sprintf("%s/%04d", name, id))
	task := &Task{agentPath: path, done: make(chan struct{})}
	j := &job{spec: spec, input: input, path: path, sink: sink, task: task}

	// Hold sendMu for reading across the send so Close cannot close tasks
	// mid-send. The closed check (under the same lock) rejects submits after
	// Close without racing the channel close.
	p.sendMu.RLock()
	defer p.sendMu.RUnlock()
	if p.closed {
		return nil, ErrPoolClosed
	}
	select {
	case <-p.ctx.Done():
		return nil, ErrPoolClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case p.tasks <- j:
		return task, nil
	}
}

// runJob executes one job on a fresh runtime, forwards its events onto the
// merged pool stream tagged with the job's AgentPath, and captures its result.
func (p *Pool) runJob(j *job) {
	defer close(j.task.done)
	rt, err := acl.NewRuntime(j.spec.config())
	if err != nil {
		j.task.err = err
		return
	}
	run, err := rt.Prompt(p.ctx, j.input)
	if err != nil {
		j.task.err = err
		return
	}
	// Forward this run's events onto the destination stream, tagged with the
	// job's path (prepended so any nested path is preserved). Jobs submitted
	// via SubmitTo use a caller-provided sink; others use the merged pool stream.
	dst := p.stream
	if j.sink != nil {
		dst = j.sink
	}
	for ev := range run.Events() {
		ev.AgentPath = prependPath(j.path, ev.AgentPath)
		dst.Send(ev)
	}
	res, werr := run.Wait() // events already drained above; returns the captured result
	j.task.res = Result{AgentPath: j.path, Messages: res.Messages, Summary: res.Summary}
	j.task.err = werr
}

// prependPath returns base followed by existing (for nested subagent paths).
func prependPath(base, existing []string) []string {
	if len(existing) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(existing))
	out = append(out, base...)
	out = append(out, existing...)
	return out
}

// Close stops accepting new tasks. In-flight and queued tasks still run; the
// merged event stream closes once they finish. Idempotent. Safe to call
// concurrently with Submit (a racing Submit returns ErrPoolClosed rather than
// panicking).
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		p.sendMu.Lock()
		p.closed = true
		close(p.tasks)
		p.sendMu.Unlock()
	})
}

// Cancel aborts all in-flight and queued tasks by cancelling the pool context,
// then behaves like Close. In-flight runs are interrupted (ctx -> abort).
func (p *Pool) Cancel() {
	p.cancel()
	p.Close()
}

// Wait blocks until all submitted tasks finish (call Close first). It drains
// the merged event stream so a caller that ignores Events never deadlocks.
func (p *Pool) Wait() error {
	for range p.stream.Events() {
	}
	return p.g.Wait()
}

// Events returns the merged, AgentPath-tagged event stream of all subagent
// runs. It closes after Close and all tasks finish. Consume it, or call Wait
// (which drains it); do not do both concurrently.
func (p *Pool) Events() <-chan event.Event { return p.stream.Events() }
