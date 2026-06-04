# gyr Daemon (v1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build gyr v1, a personal reactive ops daemon: Telegram in/out, ops tools (service control, reminders, bash), dangerous actions confirmed by inline button, everything recorded in the jess provenance ledger.

**Architecture:** A rookery-scaffolded `gyrd`/`gyrctl` pair (perch, launchd, `--no-web`). `gyrd` runs one jess agent (cloud Claude via `agentcore/llm`, the durable SQLite ledger, the ops tools, and a Telegram-backed approver) plus a Telegram long-poll loop and a reminder scheduler. The core mechanism is an async approver bridge: jess's synchronous `Approver` blocks on a channel while a Telegram confirm round-trips.

**Tech Stack:** Go 1.26, `github.com/guygrigsby/jess` (+ `jess/ledger`, `jess/gate`), `github.com/guygrigsby/perch` (via rookery), `github.com/voocel/agentcore` + `agentcore/llm` (cloud model), `github.com/go-telegram/bot` (Telegram), `modernc.org/sqlite` (reminders; ledger brings its own).

**Spec:** `docs/specs/2026-06-04-gyr-daemon-design.md` (staged in jess; Task 1 moves it into `gyr/docs/specs/`).

**Verified APIs (use these):**
- Cloud model: `import acllm "github.com/voocel/agentcore/llm"`; `m, err := acllm.NewModel("anthropic", "claude-...", acllm.WithAPIKey(key))` returns `*acllm.LiteLLMAdapter` which is an `agentcore.ChatModel`. Pass to `jess.WithModel(m)`.
- jess agent: `jess.New(jess.WithModel(m), jess.WithTools(tools...), jess.WithApprover(approver), jess.WithLedger(led), jess.WithAgentID("gyr"))` returns `*agentcore.Agent`. Drive with `jess.Stream(ctx, agent, text) (<-chan ac.Event, func() *ac.RunSummary)`.
- Approver: `type jess.Approver = gate.Approver = func(ctx context.Context, r gate.Request) (allow bool, reason string)`. `gate.Request{Tool, Label, Preview string; Args []byte}`. `jess.Request = gate.Request`.
- SafeTool marker: a tool implements `Safe() bool` returning true to skip the gate (jess.SafeTool / gate.SafeTool).
- Ledger: `ledger.OpenSQLite(path) (*ledger.SQLite, error)` is the `DurableSink`; `(*SQLite).Chain(runID) (ledger.Chain, error)` for `gyrctl why`.
- perch (from rookery): `config.Load("gyr", &cfg)`, `config.Dir("gyr")`, `daemon.SignalContext()`, `daemon.Serve(ctx, srv, timeout)`.

**File structure (gyr repo, after scaffold):**
- `cmd/gyrd/main.go` — daemon entrypoint: config, ledger, agent, telegram loop, scheduler, loopback API, graceful shutdown.
- `cmd/gyrctl/main.go` — CLI: status, reminders, why.
- `internal/config/config.go` — gyr config shape.
- `internal/agent/build.go` — builds the jess agent from config (model + tools + approver + ledger).
- `internal/confirm/confirm.go` — the async approver bridge (registry + jess.Approver).
- `internal/telegram/channel.go` — long-poll, allowlist, dispatch (message → agent, callback → confirm).
- `internal/tools/service.go`, `internal/tools/bash.go`, `internal/tools/reminders.go` — the ops tools.
- `internal/reminders/store.go`, `internal/reminders/scheduler.go` — reminders persistence + scheduler.
- `internal/api/api.go` — loopback HTTP API for gyrctl (extends rookery's).
- tests alongside each.

---

## Task 1: Scaffold gyr from rookery

**Files:** the whole new `gyr` repo.

- [ ] **Step 1: Clone rookery and rename to gyr**

```bash
cd /Users/guygrigsby/projects
git clone https://github.com/guygrigsby/rookery gyr 2>/dev/null || cp -R rookery gyr
cd gyr
rm -rf .git && git init -q
scripts/init.sh gyr --no-web
```
`init.sh gyr --no-web` renames the `app`/`App`/`APP` tokens to `gyr`/`Gyr`/`GYR`, produces `cmd/gyrd` + `cmd/gyrctl`, and strips the web/SPA. Read `scripts/init.sh` output; if `init.sh` expects to run inside a fresh clone, follow its README.

- [ ] **Step 2: Add jess + telegram dependencies**

```bash
go get github.com/guygrigsby/jess@latest
go get github.com/go-telegram/bot@latest
go mod tidy
```
(jess is on github; if it must be a local replace during dev, add `replace github.com/guygrigsby/jess => ../jess` to go.mod.)

- [ ] **Step 3: Move the spec + plan into the repo**

```bash
mkdir -p docs/specs docs/plans
cp ../jess/docs/specs/2026-06-04-gyr-daemon-design.md docs/specs/
cp ../jess/docs/plans/2026-06-04-gyr-daemon.md docs/plans/
```

- [ ] **Step 4: Confirm the scaffold builds and gates green**

Run: `go build ./... && make check` (or `make lint && make test` if no `check` target).
Expected: green (the bare scaffold). Fix any rename fallout.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "scaffold: gyr from rookery (--no-web); add jess + telegram deps"
```

---

## Task 2: Config

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

```go
package config

import "testing"

func TestDefaultsAndValidate(t *testing.T) {
	c := Default()
	if c.Confirm.Timeout != "2m" {
		t.Fatalf("default confirm timeout: %q", c.Confirm.Timeout)
	}
	// empty token / allowlist is invalid.
	if err := c.Validate(); err == nil {
		t.Fatal("empty token+allowlist must be invalid")
	}
	c.Telegram.Token = "t"
	c.Telegram.AllowedChatIDs = []int64{1}
	c.Model.Provider = "anthropic"
	c.Model.Model = "claude-x"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if _, err := c.ConfirmTimeout(); err != nil {
		t.Fatalf("parse timeout: %v", err)
	}
}
```

- [ ] **Step 2: Run, confirm fail.** `go test ./internal/config/` → FAIL.

- [ ] **Step 3: Implement `internal/config/config.go`**

```go
// Package config is gyr's config.toml shape and validation.
package config

import (
	"errors"
	"time"
)

type Config struct {
	Telegram TelegramConfig `toml:"telegram"`
	Model    ModelConfig    `toml:"model"`
	Paths    PathsConfig    `toml:"paths"`
	Confirm  ConfirmConfig  `toml:"confirm"`
}

type TelegramConfig struct {
	Token          string  `toml:"token"`
	AllowedChatIDs []int64 `toml:"allowed_chat_ids"`
}
type ModelConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	APIKey   string `toml:"api_key"`
	BaseURL  string `toml:"base_url"`
}
type PathsConfig struct {
	Ledger    string `toml:"ledger"`
	Reminders string `toml:"reminders"`
}
type ConfirmConfig struct {
	Timeout string `toml:"timeout"`
}

// Default returns config with safe defaults; secrets/allowlist still required.
func Default() Config {
	return Config{
		Model:   ModelConfig{Provider: "anthropic"},
		Paths:   PathsConfig{Ledger: "ledger.db", Reminders: "reminders.db"},
		Confirm: ConfirmConfig{Timeout: "2m"},
	}
}

func (c Config) Validate() error {
	if c.Telegram.Token == "" {
		return errors.New("config: telegram.token is required")
	}
	if len(c.Telegram.AllowedChatIDs) == 0 {
		return errors.New("config: telegram.allowed_chat_ids must list at least one chat id (the perimeter)")
	}
	if c.Model.Provider == "" || c.Model.Model == "" {
		return errors.New("config: model.provider and model.model are required")
	}
	return nil
}

func (c Config) ConfirmTimeout() (time.Duration, error) { return time.ParseDuration(c.Confirm.Timeout) }

// Allowed reports whether chatID may drive gyr.
func (c Config) Allowed(chatID int64) bool {
	for _, id := range c.Telegram.AllowedChatIDs {
		if id == chatID {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests** → PASS.
- [ ] **Step 5: Commit** `git commit -am "feat(config): gyr config shape, validation, allowlist, confirm timeout"`

---

## Task 3: The async approver bridge

**Files:**
- Create: `internal/confirm/confirm.go`
- Test: `internal/confirm/confirm_test.go`

**Context:** jess's `Approver` is synchronous and blocks the tool call. This bridges it to an async Telegram confirm: it sends a message (via an injected `Sender`), registers a channel, and blocks until a callback resolves it or it times out (deny, fail-closed).

- [ ] **Step 1: Write the failing test**

```go
package confirm

import (
	"context"
	"testing"
	"time"

	"github.com/guygrigsby/jess/gate"
)

type fakeSender struct{ lastID, lastText string }

func (f *fakeSender) SendConfirm(ctx context.Context, id, text string) error {
	f.lastID, f.lastText = id, text
	return nil
}

func TestApproveResolves(t *testing.T) {
	s := &fakeSender{}
	b := New(s, 2*time.Second)
	go func() {
		// wait until the confirm is registered, then approve it.
		time.Sleep(20 * time.Millisecond)
		b.Resolve(s.lastID, true)
	}()
	allow, _ := b.Approver()(context.Background(), gate.Request{Tool: "restart_service", Preview: "launchctl ... nginx"})
	if !allow {
		t.Fatal("expected approval")
	}
	if s.lastText == "" {
		t.Fatal("a confirm message should have been sent")
	}
}

func TestTimeoutDenies(t *testing.T) {
	b := New(&fakeSender{}, 30*time.Millisecond)
	allow, reason := b.Approver()(context.Background(), gate.Request{Tool: "bash"})
	if allow {
		t.Fatal("timeout must deny (fail-closed)")
	}
	if reason == "" {
		t.Fatal("denial should carry a reason")
	}
}

func TestDenyResolves(t *testing.T) {
	s := &fakeSender{}
	b := New(s, time.Second)
	go func() { time.Sleep(20 * time.Millisecond); b.Resolve(s.lastID, false) }()
	allow, _ := b.Approver()(context.Background(), gate.Request{Tool: "bash"})
	if allow {
		t.Fatal("explicit deny must deny")
	}
}
```

- [ ] **Step 2: Run, confirm fail.**

- [ ] **Step 3: Implement `internal/confirm/confirm.go`**

```go
// Package confirm bridges jess's synchronous Approver to an asynchronous
// Telegram confirm: it sends a confirm message, then blocks the tool call until
// a callback resolves it or it times out (deny, fail-closed).
package confirm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/guygrigsby/jess/gate"
)

// Sender sends a confirm prompt to the operator. id is the correlation id the
// callback will carry back; text is the human-readable action.
type Sender interface {
	SendConfirm(ctx context.Context, id, text string) error
}

// Bridge holds pending confirmations.
type Bridge struct {
	sender  Sender
	timeout time.Duration
	mu      sync.Mutex
	pending map[string]chan bool
	seq     int
}

func New(s Sender, timeout time.Duration) *Bridge {
	return &Bridge{sender: s, timeout: timeout, pending: map[string]chan bool{}}
}

func (b *Bridge) nextID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	return fmt.Sprintf("c%d", b.seq)
}

// Approver returns the jess.Approver. For each non-safe call it sends a confirm
// and blocks until resolved, the context is cancelled, or the timeout elapses.
func (b *Bridge) Approver() gate.Approver {
	return func(ctx context.Context, r gate.Request) (bool, string) {
		id := b.nextID()
		ch := make(chan bool, 1)
		b.mu.Lock()
		b.pending[id] = ch
		b.mu.Unlock()
		defer func() { b.mu.Lock(); delete(b.pending, id); b.mu.Unlock() }()

		text := r.Tool
		if r.Preview != "" {
			text += ": " + r.Preview
		} else if len(r.Args) > 0 {
			text += " " + string(r.Args)
		}
		if err := b.sender.SendConfirm(ctx, id, text); err != nil {
			return false, "could not send confirm: " + err.Error()
		}

		select {
		case ok := <-ch:
			if ok {
				return true, "operator approved"
			}
			return false, "operator denied"
		case <-time.After(b.timeout):
			return false, "confirm timed out"
		case <-ctx.Done():
			return false, "run cancelled"
		}
	}
}

// Resolve delivers a decision for a pending confirm id (called by the Telegram
// callback handler). Unknown or already-resolved ids are ignored.
func (b *Bridge) Resolve(id string, allow bool) {
	b.mu.Lock()
	ch := b.pending[id]
	b.mu.Unlock()
	if ch != nil {
		select {
		case ch <- allow:
		default:
		}
	}
}
```

- [ ] **Step 4: Run tests** (`go test -race ./internal/confirm/`) → PASS.
- [ ] **Step 5: Commit** `git commit -am "feat(confirm): async approver bridge (sync Approver blocks on a Telegram confirm; timeout denies)"`

---

## Task 4: Ops tools

**Files:**
- Create: `internal/tools/service.go`, `internal/tools/bash.go`
- Test: `internal/tools/service_test.go`, `internal/tools/bash_test.go`

**Context:** Each tool is an `agentcore.Tool` (Name/Description/Schema/Execute). Safe (read-only) tools implement `Safe() bool` returning true so the gate skips them; non-safe tools omit it (or return false) so they are confirmed + recorded. Shell-out is injected (a `Runner`) for testability.

- [ ] **Step 1: Write `internal/tools/service_test.go`**

```go
package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeRunner struct{ gotName, gotArgs string }

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.gotName = name
	f.gotArgs = ""
	for _, a := range args {
		f.gotArgs += a + " "
	}
	return []byte("ok"), nil
}

func TestServiceStatusIsSafe(t *testing.T) {
	var tl any = NewServiceStatus(&fakeRunner{})
	if s, ok := tl.(interface{ Safe() bool }); !ok || !s.Safe() {
		t.Fatal("service_status must be a SafeTool")
	}
}

func TestRestartServiceIsNotSafeAndRuns(t *testing.T) {
	fr := &fakeRunner{}
	rt := NewRestartService(fr)
	if s, ok := any(rt).(interface{ Safe() bool }); ok && s.Safe() {
		t.Fatal("restart_service must NOT be safe (it mutates)")
	}
	_, err := rt.Execute(context.Background(), json.RawMessage(`{"name":"nginx"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if fr.gotName != "launchctl" {
		t.Fatalf("expected launchctl, got %q", fr.gotName)
	}
}
```

- [ ] **Step 2: Run, confirm fail.**

- [ ] **Step 3: Implement `internal/tools/service.go`**

```go
// Package tools holds gyr's narrow, specific ops tools. Safe tools implement
// Safe()==true so the gate skips them; non-safe tools are confirmed + recorded.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// Runner shells out; injected for testability.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the real runner.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type nameArgs struct {
	Name string `json:"name"`
}

func objSchema(prop string) map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{prop: map[string]any{"type": "string"}},
		"required":   []string{prop}}
}

// serviceStatus — read-only, SafeTool.
type serviceStatus struct{ r Runner }

func NewServiceStatus(r Runner) *serviceStatus { return &serviceStatus{r} }
func (serviceStatus) Name() string             { return "service_status" }
func (serviceStatus) Description() string       { return "Report the status of a launchd service by name." }
func (serviceStatus) Schema() map[string]any    { return objSchema("name") }
func (serviceStatus) Safe() bool                { return true }
func (s serviceStatus) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var a nameArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Name == "" {
		return nil, fmt.Errorf("service_status: need a service name")
	}
	out, err := s.r.Run(ctx, "launchctl", "print", a.Name)
	if err != nil {
		return mustJSON(map[string]string{"status": string(out), "error": err.Error()}), nil
	}
	return mustJSON(map[string]string{"status": string(out)}), nil
}

// restartService — mutating, NON-safe.
type restartService struct{ r Runner }

func NewRestartService(r Runner) *restartService { return &restartService{r} }
func (restartService) Name() string              { return "restart_service" }
func (restartService) Description() string        { return "Restart a launchd service by name." }
func (restartService) Schema() map[string]any     { return objSchema("name") }
func (s restartService) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var a nameArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Name == "" {
		return nil, fmt.Errorf("restart_service: need a service name")
	}
	out, err := s.r.Run(ctx, "launchctl", "kickstart", "-k", a.Name)
	if err != nil {
		return nil, fmt.Errorf("restart_service %q: %w: %s", a.Name, err, out)
	}
	return mustJSON(map[string]string{"restarted": a.Name}), nil
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
```

(Add `stop_service` analogously, `launchctl bootout`, non-safe. Keep it in service.go.)

- [ ] **Step 4: Write `internal/tools/bash.go`** (non-safe, always confirmed by the gate):

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

type bash struct{ r Runner }

func NewBash(r Runner) *bash       { return &bash{r} }
func (bash) Name() string          { return "bash" }
func (bash) Description() string    { return "Run a shell command. Every call requires operator confirmation." }
func (bash) Schema() map[string]any { return objSchema("command") }

// no Safe() method => non-safe => always gated + recorded.
func (b bash) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.Command == "" {
		return nil, fmt.Errorf("bash: need a command")
	}
	out, err := b.r.Run(ctx, "/bin/sh", "-c", a.Command)
	if err != nil {
		return nil, fmt.Errorf("bash: %w: %s", err, out)
	}
	return mustJSON(map[string]string{"output": string(out)}), nil
}
```

(`objSchema("command")` reuses the helper.)

- [ ] **Step 5: Run tests** (`go test -race ./internal/tools/`) → PASS.
- [ ] **Step 6: Commit** `git commit -am "feat(tools): service_status (safe), restart/stop_service + bash (non-safe), injected runner"`

---

## Task 5: Reminders store + scheduler

**Files:**
- Create: `internal/reminders/store.go`, `internal/reminders/scheduler.go`
- Create: `internal/tools/reminders.go` (the tools, SafeTool)
- Test: `internal/reminders/store_test.go`, `internal/reminders/scheduler_test.go`

**Context:** A SQLite-backed store of reminders + a scheduler that fires due reminders via an injected `Notifier`, with a fake clock for tests. Setting/listing/cancelling reminders are SafeTools (scheduling a future ping is not dangerous).

- [ ] **Step 1: Write `internal/reminders/store_test.go`**

```go
package reminders

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreAddListCancelDue(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	due := time.Unix(1000, 0).UTC()
	id, err := st.Add("call mom", due, 42)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := st.Pending()
	if len(list) != 1 || list[0].Text != "call mom" {
		t.Fatalf("pending: %+v", list)
	}
	d, _ := st.DueBefore(time.Unix(1001, 0).UTC())
	if len(d) != 1 {
		t.Fatalf("due: %+v", d)
	}
	if err := st.MarkFired(id); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.DueBefore(time.Unix(1001, 0).UTC()); len(d) != 0 {
		t.Fatal("fired reminder should not be due")
	}
	if err := st.Cancel(id); err != nil { // idempotent
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run, confirm fail.**

- [ ] **Step 3: Implement `internal/reminders/store.go`**

```go
// Package reminders persists reminders and fires them on a schedule.
package reminders

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Reminder struct {
	ID    int64
	Text  string
	Due   time.Time
	Chat  int64
}

type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS reminders(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  text TEXT NOT NULL, due_at INTEGER NOT NULL, chat_id INTEGER NOT NULL,
  fired_at INTEGER);`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Add(text string, due time.Time, chat int64) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO reminders(text,due_at,chat_id) VALUES(?,?,?)`, text, due.Unix(), chat)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
func (s *Store) Cancel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM reminders WHERE id=?`, id)
	return err
}
func (s *Store) MarkFired(id int64) error {
	_, err := s.db.Exec(`UPDATE reminders SET fired_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}
func (s *Store) Pending() ([]Reminder, error)              { return s.query(`fired_at IS NULL`) }
func (s *Store) DueBefore(t time.Time) ([]Reminder, error) {
	return s.query(`fired_at IS NULL AND due_at <= ` + itoa(t.Unix()))
}
func (s *Store) query(where string) ([]Reminder, error) {
	rows, err := s.db.Query(`SELECT id,text,due_at,chat_id FROM reminders WHERE ` + where + ` ORDER BY due_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		var r Reminder
		var due int64
		if err := rows.Scan(&r.ID, &r.Text, &due, &r.Chat); err != nil {
			return nil, err
		}
		r.Due = time.Unix(due, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
```

(Add a small `itoa(int64) string` helper using `strconv.FormatInt`. NOTE: `DueBefore` interpolates an integer it produced itself, not user input, so it is injection-safe; prefer a parameterized query if you make it accept arbitrary values.)

- [ ] **Step 4: Write `internal/reminders/scheduler.go`** with a fake-clock-friendly loop:

```go
package reminders

import (
	"context"
	"time"
)

// Notifier delivers a fired reminder.
type Notifier interface {
	Notify(ctx context.Context, chat int64, text string) error
}

// Scheduler fires due reminders. tick controls the poll interval (small in
// tests). It reloads from the store each tick, so it survives restart simply by
// being started against the same store.
type Scheduler struct {
	store    *Store
	notifier Notifier
	now      func() time.Time
	tick     time.Duration
}

func NewScheduler(s *Store, n Notifier, tick time.Duration) *Scheduler {
	return &Scheduler{store: s, notifier: n, now: time.Now, tick: tick}
}

// RunOnce fires everything due as of now; returns count fired. Exposed for tests.
func (sc *Scheduler) RunOnce(ctx context.Context) (int, error) {
	due, err := sc.store.DueBefore(sc.now())
	if err != nil {
		return 0, err
	}
	for _, r := range due {
		if err := sc.notifier.Notify(ctx, r.Chat, "Reminder: "+r.Text); err != nil {
			continue // try again next tick
		}
		_ = sc.store.MarkFired(r.ID)
	}
	return len(due), nil
}

// Run polls until ctx is done.
func (sc *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(sc.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = sc.RunOnce(ctx)
		}
	}
}
```

- [ ] **Step 5: Write `internal/reminders/scheduler_test.go`** (fake clock + fake notifier):

```go
package reminders

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type fakeNotifier struct{ msgs []string }

func (f *fakeNotifier) Notify(_ context.Context, _ int64, text string) error {
	f.msgs = append(f.msgs, text)
	return nil
}

func TestSchedulerFiresDueAndMarksFired(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "r.db"))
	defer st.Close()
	_, _ = st.Add("call mom", time.Unix(100, 0).UTC(), 7)
	n := &fakeNotifier{}
	sc := NewScheduler(st, n, time.Millisecond)
	sc.now = func() time.Time { return time.Unix(200, 0).UTC() } // past due

	if got, _ := sc.RunOnce(context.Background()); got != 1 {
		t.Fatalf("expected 1 fired, got %d", got)
	}
	if len(n.msgs) != 1 || n.msgs[0] != "Reminder: call mom" {
		t.Fatalf("notify: %+v", n.msgs)
	}
	// fired -> not due again (survives a re-run / restart)
	if got, _ := sc.RunOnce(context.Background()); got != 0 {
		t.Fatal("a fired reminder must not fire twice")
	}
}
```

- [ ] **Step 6: Write `internal/tools/reminders.go`** — `set_reminder`/`list_reminders`/`cancel_reminder`, all SafeTool, backed by `*reminders.Store`. `set_reminder` parses a `when` (RFC3339 or a duration like "2h"); on parse fail, return an error result. Each implements `Safe() bool { return true }`. (Show the same Tool shape as service.go; the reminder tools call `store.Add/Pending/Cancel`.)

- [ ] **Step 7: Run tests** (`go test -race ./internal/reminders/ ./internal/tools/`) → PASS.
- [ ] **Step 8: Commit** `git commit -am "feat(reminders): SQLite store + scheduler (fires due, survives restart) + safe reminder tools"`

---

## Task 6: Build the agent

**Files:**
- Create: `internal/agent/build.go`
- Test: `internal/agent/build_test.go`

**Context:** Assemble the jess agent from config: the cloud model, the tools, the approver, the durable ledger.

- [ ] **Step 1: Write the failing test** (uses `jess.Once` to avoid a network model, asserts Build returns a usable agent + that a non-safe tool is denied with a deny-all approver):

```go
package agent

import (
	"context"
	"path/filepath"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/ledger"
)

func TestBuildWithExplicitModelAndLedger(t *testing.T) {
	led, _ := ledger.OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	defer led.Close()
	model := jess.Once(true, func(context.Context, []ac.Message, []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Message: ac.Message{Role: ac.RoleAssistant, Content: []ac.ContentBlock{ac.TextBlock("hi")}, StopReason: ac.StopReasonStop}}, nil
	})
	deny := func(context.Context, jess.Request) (bool, string) { return false, "no" }
	ag := Build(Deps{Model: model, Ledger: led, Approver: deny, Tools: nil})
	if ag == nil {
		t.Fatal("nil agent")
	}
}
```

- [ ] **Step 2: Run, confirm fail.**

- [ ] **Step 3: Implement `internal/agent/build.go`**

```go
// Package agent assembles gyr's jess agent.
package agent

import (
	ac "github.com/voocel/agentcore"
	acllm "github.com/voocel/agentcore/llm"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/ledger"

	"github.com/guygrigsby/gyr/internal/config"
)

// Deps are the pre-built pieces (model, ledger, approver, tools). Build wires
// them into a jess agent. Tests pass an explicit Model (jess.Once); production
// uses Model from config via NewCloudModel.
type Deps struct {
	Model    ac.ChatModel
	Ledger   ledger.DurableSink
	Approver jess.Approver
	Tools    []ac.Tool
}

func Build(d Deps) *ac.Agent {
	return jess.New(
		jess.WithModel(d.Model),
		jess.WithTools(d.Tools...),
		jess.WithApprover(d.Approver),
		jess.WithLedger(d.Ledger),
		jess.WithAgentID("gyr"),
	)
}

// NewCloudModel builds the litellm-backed cloud model from config.
func NewCloudModel(c config.ModelConfig) (ac.ChatModel, error) {
	opts := []acllm.ModelOption{}
	if c.APIKey != "" {
		opts = append(opts, acllm.WithAPIKey(c.APIKey))
	}
	if c.BaseURL != "" {
		opts = append(opts, acllm.WithBaseURL(c.BaseURL))
	}
	return acllm.NewModel(c.Provider, c.Model, opts...)
}
```

- [ ] **Step 4: Run tests** → PASS. (Confirm `ac.Tool`, `acllm.ModelOption`, `acllm.WithAPIKey/WithBaseURL/NewModel` names against the cache; they were verified to exist.)
- [ ] **Step 5: Commit** `git commit -am "feat(agent): build the jess agent (cloud model, tools, approver, ledger)"`

---

## Task 7: Telegram channel

**Files:**
- Create: `internal/telegram/channel.go`
- Test: `internal/telegram/channel_test.go`

**Context:** Long-poll with `github.com/go-telegram/bot`. The channel: enforces the allowlist, turns an allowed text message into a `jess.Stream` run and replies, routes button callbacks to the confirm bridge, implements the `confirm.Sender` (send a message with Approve/Deny inline buttons) and the `reminders.Notifier` (send a plain message). One run at a time; a request arriving mid-run gets a "busy" reply.

This task has external-library surface (go-telegram/bot). Structure it so the agent-driving and dispatch logic is testable WITHOUT the live bot: define a small `Sender`/`replyFunc` seam and unit-test the dispatch (allowlist, busy, callback→resolve) with fakes; keep the thin `bot`-wiring (RegisterHandler, Start) in a separate, un-unit-tested `Run` method exercised by the integration test (Task 9).

- [ ] **Step 1: Write `internal/telegram/channel_test.go`** for the pure dispatch logic:

```go
package telegram

import (
	"context"
	"testing"
)

type capture struct{ replies []string; confirms []string; resolved map[string]bool }

func (c *capture) reply(_ context.Context, _ int64, text string) error {
	c.replies = append(c.replies, text); return nil
}

func TestIgnoresNonAllowlistedChat(t *testing.T) {
	c := &capture{}
	d := &Dispatcher{Allowed: func(id int64) bool { return id == 1 }, Reply: c.reply,
		Run: func(context.Context, string) string { return "ran" }}
	d.OnMessage(context.Background(), 2, "restart nginx") // not allowed
	if len(c.replies) != 0 {
		t.Fatalf("non-allowed chat must be ignored, got %v", c.replies)
	}
}

func TestAllowedMessageDrivesAgentAndReplies() {} // sketch below

func TestBusyWhenRunActive(t *testing.T) {
	c := &capture{}
	started := make(chan struct{})
	release := make(chan struct{})
	d := &Dispatcher{Allowed: func(int64) bool { return true }, Reply: c.reply,
		Run: func(context.Context, string) string { close(started); <-release; return "done" }}
	go d.OnMessage(context.Background(), 1, "first")
	<-started
	d.OnMessage(context.Background(), 1, "second") // arrives mid-run
	close(release)
	// second got a busy reply (exact text asserted in impl); at least one reply is "busy".
	found := false
	for _, r := range c.replies {
		if r == busyText {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a busy reply, got %v", c.replies)
	}
}
```

(Write `TestAllowedMessageDrivesAgentAndReplies` concretely: allowed chat, `Run` returns "ran", assert the reply equals "ran".)

- [ ] **Step 2: Run, confirm fail.**

- [ ] **Step 3: Implement the dispatcher in `internal/telegram/channel.go`** (pure, testable):

```go
package telegram

import (
	"context"
	"sync"
)

const busyText = "busy with another request, try again in a moment"

// Dispatcher is the pure, testable core of the Telegram channel: allowlist,
// one-run-at-a-time, and reply. The live bot wiring (Run, below) feeds it.
type Dispatcher struct {
	Allowed func(chatID int64) bool
	Reply   func(ctx context.Context, chatID int64, text string) error
	Run     func(ctx context.Context, input string) string // drives the agent, returns the reply text

	mu     sync.Mutex
	active bool
}

func (d *Dispatcher) OnMessage(ctx context.Context, chatID int64, text string) {
	if !d.Allowed(chatID) {
		return // perimeter: ignore (caller logs)
	}
	d.mu.Lock()
	if d.active {
		d.mu.Unlock()
		_ = d.Reply(ctx, chatID, busyText)
		return
	}
	d.active = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.active = false; d.mu.Unlock() }()

	reply := d.Run(ctx, text)
	_ = d.Reply(ctx, chatID, reply)
}
```

- [ ] **Step 4: Add the live bot wiring** in the same file (a `Channel` type holding the `*bot.Bot`, the `*confirm.Bridge`, the `Dispatcher`, the allowed chat for confirms, and the agent). Implement:
  - `confirm.Sender`: `SendConfirm(ctx, id, text)` sends a message to the operator chat with an inline keyboard of two buttons whose callback data are `id+":y"` and `id+":n"`.
  - `reminders.Notifier`: `Notify(ctx, chat, text)` sends a plain message.
  - The bot handlers: a default message handler → `dispatcher.OnMessage`; a callback handler → parse `id:y|n`, `bridge.Resolve(id, yes)`, answer the callback, edit the message.
  - `Run(ctx)` → `bot.New(token, ...)`, register handlers, `b.Start(ctx)` (long-polls).
  - The `Dispatcher.Run` closure drives the agent: `ch, wait := jess.Stream(ctx, agent, input)`, drain events, collect the final assistant text from `wait()`/the events, return it (or an error string).

  CONFIRM the go-telegram/bot API names against the installed version (`bot.New`, `bot.WithDefaultHandler`, `b.SendMessage`, `models.InlineKeyboardMarkup`/`InlineKeyboardButton`, `models.Update.CallbackQuery`, `b.AnswerCallbackQuery`, `b.Start`). Adapt to the real types. This wiring is exercised by the Task 9 integration test, not unit tests.

- [ ] **Step 5: Run tests** (`go test -race ./internal/telegram/`) → the dispatcher tests PASS.
- [ ] **Step 6: Commit** `git commit -am "feat(telegram): allowlisted long-poll dispatcher (one-run-at-a-time) + confirm sender + reminder notifier"`

---

## Task 8: Daemon main + loopback API

**Files:**
- Modify: `cmd/gyrd/main.go`
- Modify: `internal/api/api.go` (add status / reminders / why endpoints over loopback)

- [ ] **Step 1: Wire `cmd/gyrd/main.go`**: load config (`config.Load("gyr", &cfg)`), `cfg.Validate()`, resolve paths under `config.Dir("gyr")`, open the ledger, open the reminders store, build the confirm bridge + telegram channel (so the channel is the confirm Sender and the reminder Notifier), build the tools (service/bash/reminder tools sharing the runner + store), build the cloud model (`agent.NewCloudModel`), build the agent (`agent.Build`), wire `dispatcher.Run` to drive the agent. Start: the telegram `Run(ctx)`, the scheduler `Run(ctx)`, and the loopback HTTP API (`daemon.Serve`), all under `daemon.SignalContext()`. Graceful shutdown closes the ledger + reminders store.

- [ ] **Step 2: Extend `internal/api/api.go`**: add loopback JSON endpoints `GET /status`, `GET /reminders`, `GET /why?run=<id>` (reads `ledger.Chain(run)`), guarded by perch's existing loopback token auth. Write a small test for the `/why` handler against a temp ledger with one recorded run.

- [ ] **Step 3: Run** `go build ./... && go vet ./... && make check`. Then a manual smoke (no real token needed): `gyrd` with an invalid config should exit with the validation error.

- [ ] **Step 4: Commit** `git commit -am "feat(gyrd): wire daemon (telegram + scheduler + loopback api); ledger why endpoint"`

---

## Task 9: gyrctl + integration test

**Files:**
- Modify: `cmd/gyrctl/main.go`
- Create: `internal/integration_test.go` (or `cmd/gyrd/integration_test.go`)

- [ ] **Step 1: gyrctl subcommands** `status`, `reminders`, `why <run-id>` — each calls the loopback API (perch client) and prints. `why` prints the chain triad (request, available, actions with verdicts).

- [ ] **Step 2: Integration test** (no network, no real Telegram): build the agent with a `jess.Once` model that emits one `restart_service` tool call then stops; wire a real `confirm.Bridge` with a fake `Sender` that immediately `Resolve`s approve; a fake `Runner` for launchctl; a real temp SQLite ledger. Drive `dispatcher.Run(ctx, "restart nginx")`. Assert: the fake runner saw `launchctl kickstart`, the confirm was sent, and `ledger.Chain(runID)` reconstructs the action with `verdict=allowed`. Add a second case: the fake Sender resolves DENY → the runner is NOT called and the chain shows the denied attempt.

- [ ] **Step 3: Run** `go test -race ./... && make check` → green.
- [ ] **Step 4: Commit** `git commit -am "feat(gyrctl): status/reminders/why; integration test (confirm -> action -> ledger chain)"`

---

## Task 10: Config example, launchd, docs

- [ ] **Step 1:** Write `config.example.toml` with the gyr shape (telegram token/allowlist, model, paths, confirm timeout) and a comment that secrets should come from env / 1Password.
- [ ] **Step 2:** Confirm the rookery launchd target (`deploy/`) is renamed for gyr and points at `gyrd`; document install in `README.md` (`gyrctl`, the allowlist, getting a bot token from @BotFather, finding your chat id).
- [ ] **Step 3:** Update `CLAUDE.md`/`AGENTS.md` (from rookery) to describe gyr: the reactive ops daemon on jess + ledger, the allowlist perimeter, bash-always-confirms, the deferred ambient v2.
- [ ] **Step 4: Final gate** `go vet ./... && make check`. Commit.

---

## Self-Review notes

- Spec coverage: scaffold (T1), config+allowlist (T2), approver bridge (T3), tools incl bash-always-confirm + SafeTool split (T4), reminders store+scheduler+tools (T5), agent build incl cloud model (T6), telegram long-poll/allowlist/one-run/confirm-sender/notifier (T7), daemon wiring + why endpoint (T8), gyrctl why + integration confirm→action→chain (T9), config/launchd/docs (T10). All spec sections mapped.
- The external-library seam (go-telegram/bot) is isolated in T7's live wiring and exercised by T9's integration test; the pure dispatch + confirm bridge are unit-tested with fakes, so correctness does not depend on mocking the bot. Confirm the exact go-telegram/bot API names against the installed version before writing T7 step 4.
- The model API (`acllm.NewModel` / `WithAPIKey` / `WithBaseURL`) and jess surface (`WithApprover`, `WithLedger`, `Request`, `Stream`, `Once`) were verified against the installed deps.
- Green-between-tasks: T2-T6 are independent packages (config, confirm, tools, reminders, agent) that build and test in isolation; T7's dispatcher is pure; T8-T9 integrate. Each task ends with its package green; T8 restores whole-binary green; T9 is the end-to-end proof.
