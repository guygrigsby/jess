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
type Pool struct {
	mu       sync.RWMutex
	specs    map[string]Spec
	maxDepth int

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

// Submit queues a run of the named subagent with the given input. It blocks if
// the queue is full and returns when a slot is available or ctx is cancelled.
// parentPath is the caller's AgentPath (nil at top level).
func (p *Pool) Submit(ctx context.Context, name, input string, parentPath ...string) (*Task, error) {
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
	j := &job{spec: spec, input: input, path: path, task: task}

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
	// Forward this run's events onto the merged stream, tagged with the job's
	// path (prepended so any nested path is preserved).
	for ev := range run.Events() {
		ev.AgentPath = prependPath(j.path, ev.AgentPath)
		p.stream.Send(ev)
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
// merged event stream closes once they finish. Idempotent.
func (p *Pool) Close() {
	p.closeOnce.Do(func() { close(p.tasks) })
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
