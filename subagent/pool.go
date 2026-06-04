package subagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/internal/core"
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
	base     core.Config // parent defaults inherited by each Spec (model, gate, audit)

	// sendMu guards sends to / closing of tasks. Submit holds it for reading
	// across its channel send; Close holds it for writing while it closes the
	// channel, so a send can never race the close (no send-on-closed panic).
	// closing is closed by Close BEFORE it takes sendMu, so a Submit blocked on
	// a full queue unblocks immediately rather than holding the read lock and
	// stalling Close.
	sendMu  sync.RWMutex
	closed  bool
	closing chan struct{}

	tasks  chan *job
	stream *Stream
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
	sink  *Stream // nil => the pool's merged stream
	task  *Task
	ctx   context.Context // the Submit/SubmitTo ctx; aborts the running job when cancelled
}

// Option configures a Pool.
type Option func(*poolConfig)

type poolConfig struct {
	maxConcurrent, maxQueued, maxDepth int
	base                               core.Config
}

// WithMaxConcurrent caps how many subagent runs execute at once (default 8).
func WithMaxConcurrent(n int) Option { return func(c *poolConfig) { c.maxConcurrent = n } }

// WithMaxQueued caps how many tasks may wait in the queue; Submit blocks when
// full (default 1024).
func WithMaxQueued(n int) Option { return func(c *poolConfig) { c.maxQueued = n } }

// WithMaxDepth caps subagent nesting depth (default 8).
func WithMaxDepth(n int) Option { return func(c *poolConfig) { c.maxDepth = n } }

// WithDefaults sets the parent defaults each Spec inherits when its
// corresponding field is unset: the model, tool gate, audit sink, and agentID.
// jess.New uses this so subagents share the parent agent's safety controls.
func WithDefaults(model ac.ChatModel, gate ac.ToolGate, sink ledger.Sink, agentID string) Option {
	return func(c *poolConfig) {
		c.base = core.Config{Model: model, Gate: gate, Audit: sink, AgentID: agentID}
	}
}

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
		base:     cfg.base,
		closing:  make(chan struct{}),
		tasks:    make(chan *job, cfg.maxQueued),
		stream:   NewStream(mergedStreamBuffer),
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
//
// The sink must be actively consumed. If the sink's buffer fills with no reader,
// the forwarding worker blocks on Send, which stalls the task until the sink
// drains.
func (p *Pool) SubmitTo(ctx context.Context, sink *Stream, name, input string, parentPath ...string) (*Task, error) {
	return p.submit(ctx, sink, name, input, parentPath...)
}

// submit is the shared implementation for Submit and SubmitTo.
func (p *Pool) submit(ctx context.Context, sink *Stream, name, input string, parentPath ...string) (*Task, error) {
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
	j := &job{spec: spec, input: input, path: path, sink: sink, task: task, ctx: ctx}

	// Hold sendMu for reading across the send so Close cannot close tasks
	// mid-send. The closed check (under the same lock) rejects submits after
	// Close without racing the channel close.
	p.sendMu.RLock()
	defer p.sendMu.RUnlock()
	if p.closed {
		return nil, ErrPoolClosed
	}
	select {
	case <-p.closing:
		// Close was called; unblock rather than hold the read lock (and stall
		// Close) waiting for queue space.
		return nil, ErrPoolClosed
	case <-p.ctx.Done():
		return nil, ErrPoolClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case p.tasks <- j:
		return task, nil
	}
}

// runJob executes one job as a fresh *agentcore.Agent, forwards its events onto
// the destination stream tagged with the job's AgentPath, and captures its
// result.
func (p *Pool) runJob(j *job) {
	defer close(j.task.done)

	// Run under a context derived from BOTH the pool ctx (so pool teardown via
	// Close/Cancel aborts the job) and the submit ctx (so cancelling
	// Submit/SubmitTo's ctx aborts an already-running job — e.g. a parent run
	// aborting its delegated subagent). The watcher exits when either ctx fires
	// or the job ends (defer cancel -> runCtx.Done), so it cannot leak; at most
	// MaxConcurrent watchers exist at once.
	runCtx, cancel := context.WithCancel(p.ctx)
	defer cancel()
	if j.ctx != nil {
		go func() {
			select {
			case <-j.ctx.Done():
				cancel()
			case <-runCtx.Done():
			}
		}()
	}

	agent := core.Agent(j.spec.config(p.base))
	defer core.ReleaseAgent(agent) // pool agents are single-use; free from registry when done
	ch, wait := core.Stream(runCtx, agent, j.input)

	// Forward this run's events onto the destination stream, tagged with the
	// job's path (prepended so any nested path is preserved). Jobs submitted via
	// SubmitTo use a caller-provided sink; others use the merged pool stream.
	//
	// Known limitation: nested subagent events are tagged with the immediate
	// job's path only — not composed across pool-nesting levels — because
	// agentcore.Event carries no path field. To be revisited if pool nesting
	// becomes a first-class use case.
	dst := p.stream
	if j.sink != nil {
		dst = j.sink
	}
	var output string
	for ev := range ch {
		if ev.Type == ac.EventMessageEnd && ev.Message.GetRole() == ac.RoleAssistant {
			if s := ev.Message.TextContent(); s != "" {
				output = s
			}
		}
		dst.Send(Event{Event: ev, AgentPath: append([]string(nil), j.path...)})
	}
	summary := wait()
	j.task.res = Result{AgentPath: j.path, Output: output, Summary: summary}
}

// Close stops accepting new tasks. In-flight and queued tasks still run; the
// merged event stream closes once they finish. Idempotent. Safe to call
// concurrently with Submit (a racing Submit returns ErrPoolClosed rather than
// panicking).
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		// Signal closure first (no lock) so any Submit blocked on a full queue
		// unblocks and releases its read lock; only then take the write lock to
		// close the channel safely (no send can be in flight).
		close(p.closing)
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
func (p *Pool) Events() <-chan Event { return p.stream.Events() }
