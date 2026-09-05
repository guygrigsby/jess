# jess/mcp adapter implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move gyr's `internal/mcp` (MCP stdio servers adapted into `agentcore.Tool`s) into jess as the shared package `github.com/guygrigsby/jess/mcp`, extend it for autophage's toolbox use, and cut gyr over to it.

**Architecture:** `jess/mcp` owns the MCP client SDK and exposes only `[]ac.Tool` plus a closer. Config is jess's own `mcp.Server` struct, not gyr's. Two additions over gyr's copy: `Server.Bare` exposes tool names without the `<server>__` prefix (autophage runs one toolbox and wants the model to see `bash`, `read`, ...), and a text result that is valid JSON passes through unwrapped instead of being wrapped in `{"output": ...}` (agentcore's own tools return JSON, and double-wrapping degrades what the model sees). gyr deletes its copy and imports `jess/mcp`.

**Tech Stack:** Go 1.26, `github.com/modelcontextprotocol/go-sdk v1.6.1` (the version gyr already pins), `github.com/voocel/agentcore v1.6.9`.

**Spec:** `/Users/guygrigsby/projects/autophage/docs/adr/0002-sandbox-posture.md` (the decision to extract) and gyr's `docs/adr/0002-tools-via-mcp.md` (the original adapter's contract). Source to move: `/Users/guygrigsby/projects/gyr/internal/mcp/{tools.go,client_sdk.go,tools_test.go,e2e_test.go}`.

## Global Constraints

- Two repos. jess: `/Users/guygrigsby/projects/jess`, module `github.com/guygrigsby/jess`, work on `main`. gyr: `/Users/guygrigsby/projects/gyr`, module `github.com/guygrigsby/gyr`, work on `main`. Both checkouts are plain git (verify with `git status`; if either shows a GitButler workspace commit at HEAD, stop and report).
- Only `jess/mcp` may import the MCP SDK. No SDK type crosses its boundary.
- `mcp` stays agentcore-dependent (it produces `ac.Tool`); that is fine, jess root already depends on agentcore.
- No em or en dashes anywhere, no Oxford commas, terse verb-first commit messages prefixed `mcp:` in jess and `mcp:` or `deps:` in gyr. No Claude or Anthropic attribution, no `Co-Authored-By` trailers.
- jess gate before each commit: `go vet ./... && go test -race ./...`. gyr gate: `make check`.
- Do not push and do not tag.

---

### Task 1: jess/mcp package with Bare names and JSON passthrough

**Files:**
- Create: `mcp/mcp.go` (from gyr's `tools.go`, adapted)
- Create: `mcp/client_sdk.go` (from gyr's `client_sdk.go`, adapted)
- Create: `mcp/doc.go`
- Test: `mcp/mcp_test.go` (from gyr's `tools_test.go`, adapted, plus two new tests)
- Modify: `go.mod` (add the SDK)

**Interfaces:**
- Produces:
  - `type Server struct { Name, Command string; Args, Env []string; Bare bool }`
  - `func Tools(ctx context.Context, servers []Server, logf func(string, ...any)) ([]ac.Tool, io.Closer, error)`
  - Unexported `client` interface `{ listTools(ctx) ([]toolDef, error); callTool(ctx, name string, args map[string]any) (string, error); io.Closer }` and `var dial dialFunc` swapped by tests, exactly as in gyr.

- [ ] **Step 1: Copy gyr's package into jess and rename**

```bash
cd /Users/guygrigsby/projects/jess && mkdir -p mcp && cp ../gyr/internal/mcp/tools.go mcp/mcp.go && cp ../gyr/internal/mcp/client_sdk.go mcp/client_sdk.go && cp ../gyr/internal/mcp/tools_test.go mcp/mcp_test.go && cp ../gyr/internal/mcp/e2e_test.go mcp/e2e_test.go
```

Then edit every copied file:
- Replace the import `"github.com/guygrigsby/gyr/internal/config"` with nothing, and every `config.MCPServer` with `Server`.
- In `client_sdk.go`, `mcpsdk.Implementation{Name: "gyr", Version: "v1"}` becomes `{Name: "jess", Version: "v1"}`.
- Package doc: replace the gyr-specific opening of `mcp.go` with the `doc.go` below and keep only the code.

- [ ] **Step 2: Write doc.go and the Server type**

`mcp/doc.go`:

```go
// Package mcp adapts Model Context Protocol servers into agentcore tools. It
// owns the MCP client SDK and its types and exposes only []ac.Tool plus a
// closer; no SDK type crosses this boundary.
//
// Trust is the caller's allowlist: Tools only connects to the servers it is
// handed. Every adapted tool is non-safe (it does not implement gate.SafeTool),
// so a jess gate confirm-gates and ledgers each call unless the host opted
// into AllowAll.
package mcp
```

Add to `mcp/mcp.go`, replacing the `config.MCPServer` uses:

```go
// Server describes one stdio MCP server to launch. Command and Args are the
// launch line; Env is optional extra environment ("K=V") appended to the
// process environment. Bare exposes the server's tool names as-is (sanitized)
// instead of prefixed with "<Name>__"; use it when the host runs a single
// server and wants the model to see the tools' own names.
type Server struct {
	Name    string
	Command string
	Args    []string
	Env     []string
	Bare    bool
}
```

- [ ] **Step 3: Write the two new failing tests**

Append to `mcp/mcp_test.go` (it already has a `fakeClient` and a `withDial` helper from gyr; reuse them, reading the file first to match their names exactly):

```go
// TestBareNamesSkipThePrefix proves Server.Bare exposes tool names unprefixed.
func TestBareNamesSkipThePrefix(t *testing.T) {
	fc := &fakeClient{defs: []toolDef{{Name: "bash", Description: "run"}, {Name: "read", Description: "read"}}}
	restore := withDial(func(context.Context, Server) (client, error) { return fc, nil })
	defer restore()

	tools, closer, err := Tools(context.Background(), []Server{{Name: "toolbox", Command: "x", Bare: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	got := []string{tools[0].Name(), tools[1].Name()}
	if got[0] != "bash" || got[1] != "read" {
		t.Errorf("names = %v, want bare", got)
	}
}

// TestJSONResultPassesThrough proves a tool result that is valid JSON is
// returned unwrapped, and plain text is still wrapped as {"output": text}.
func TestJSONResultPassesThrough(t *testing.T) {
	fc := &fakeClient{defs: []toolDef{{Name: "read"}}, result: `{"content":"hello","lines":1}`}
	restore := withDial(func(context.Context, Server) (client, error) { return fc, nil })
	defer restore()

	tools, closer, _ := Tools(context.Background(), []Server{{Name: "s", Command: "x"}}, nil)
	defer func() { _ = closer.Close() }()
	out, err := tools[0].Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"content":"hello","lines":1}` {
		t.Errorf("json result was wrapped: %s", out)
	}

	fc.result = "plain words"
	out, _ = tools[0].Execute(context.Background(), nil)
	if string(out) != `{"output":"plain words"}` {
		t.Errorf("text result = %s", out)
	}
}
```

If gyr's fake client stores its canned result under a different field name, use that name. If there is no `withDial` helper, write one:

```go
func withDial(d dialFunc) (restore func()) {
	old := dial
	dial = d
	return func() { dial = old }
}
```

- [ ] **Step 4: Run the tests to verify the new ones fail**

Run: `cd /Users/guygrigsby/projects/jess && go get github.com/modelcontextprotocol/go-sdk@v1.6.1 && go test ./mcp/ 2>&1 | tail -15`
Expected: the copied tests pass; `TestBareNamesSkipThePrefix` fails (names are `toolbox__bash`), `TestJSONResultPassesThrough` fails (wrapped).

- [ ] **Step 5: Implement Bare and passthrough**

In `Tools`, where the name is chosen:

```go
		for _, def := range defs {
			name := uniqueName(srv.Name, def.Name, srv.Bare, seen)
```

and change `uniqueName`:

```go
// uniqueName builds a sanitized tool name that satisfies anthropicToolName and
// is unique within seen, marking it used. Prefixed "<server>__<tool>" unless
// bare, in which case the tool's own name is used.
func uniqueName(server, tool string, bare bool, seen map[string]bool) string {
	raw := server + "__" + tool
	if bare {
		raw = tool
	}
	base := sanitize(raw)
	name := base
	for i := 2; seen[name]; i++ {
		suffix := fmt.Sprintf("_%d", i)
		if len(base)+len(suffix) > 64 {
			base = base[:64-len(suffix)]
		}
		name = base + suffix
	}
	seen[name] = true
	return name
}
```

Update the existing `TestNamesAreSanitizedAndUnique` call sites to pass `false`.

In `mcpTool.Execute`, replace the return:

```go
	res, err := t.client.callTool(ctx, t.realName, args)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: %w", t.name, err)
	}
	if json.Valid([]byte(res)) {
		return json.RawMessage(res), nil
	}
	return mustJSON(map[string]string{"output": res}), nil
```

- [ ] **Step 6: Run the package tests**

Run: `cd /Users/guygrigsby/projects/jess && go test -race ./mcp/ -v 2>&1 | tail -20`
Expected: all PASS. The e2e test skips without `npx`; if `npx` is present it will fetch `@modelcontextprotocol/server-everything` and must pass too.

- [ ] **Step 7: Tidy and gate**

Run: `cd /Users/guygrigsby/projects/jess && go mod tidy && gofmt -l . && go vet ./... && go test -race ./... 2>&1 | tail -8`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
cd /Users/guygrigsby/projects/jess && git add go.mod go.sum mcp/ && git commit -m "mcp: adapt MCP stdio servers into agentcore tools, with bare names and JSON passthrough"
```

---

### Task 2: jess docs: ADR, README, CHANGELOG

**Files:**
- Create: `docs/adr/0004-mcp-adapter.md`
- Modify: `README.md` (add a `jess/mcp` bullet in "What it is" and a `mcp/` entry in the layout tree)
- Modify: `CHANGELOG.md` (top entry)
- Modify: `CLAUDE.md` (package layout list)

- [ ] **Step 1: Write the ADR**

```markdown
# ADR 0004: MCP adapter as a jess package

**Status:** Accepted
**Date:** 2026-09-04

## Context

gyr built `internal/mcp` to adapt config-allowlisted MCP stdio servers into agentcore tools (gyr ADR 0002). autophage needs the same adapter to reach a toolbox process inside a podman sandbox over `podman exec -i`. Go's `internal` rule forbids importing it across modules, and a copy would fork a tested anti-corruption layer.

## Decision

Move the adapter into jess as `jess/mcp`. It owns the MCP client SDK; it exposes `Server` (jess's own config type) and `Tools(ctx, servers, logf) ([]ac.Tool, io.Closer, error)`. Two additions: `Server.Bare` exposes unprefixed tool names for single-server hosts, and a text result that is valid JSON passes through unwrapped so tools that already return JSON are not double-wrapped. Adapted tools stay non-safe; the host's gate decides.

## Consequences

- gyr and autophage share one adapter. gyr deletes `internal/mcp` and maps its config to `mcp.Server`.
- jess gains a dependency on `github.com/modelcontextprotocol/go-sdk`; only `jess/mcp` imports it, and hosts that do not import `jess/mcp` do not link it.
- A JSON-shaped text result now reaches the model unwrapped. Hosts that relied on the `{"output": ...}` envelope for JSON text see a different shape.
```

- [ ] **Step 2: README, CHANGELOG, CLAUDE.md**

README "What it is" list, after the `jess/gate` bullet:

```markdown
- **`jess/mcp`**, MCP stdio servers adapted into agentcore tools. `Tools(ctx, servers, logf)` launches each allowlisted server, lists its tools and returns them as `[]ac.Tool` plus a closer. Names are `<server>__<tool>` or bare (`Server.Bare`); JSON results pass through unwrapped. Only this package imports the MCP SDK.
```

README layout tree: add `├── mcp/                          # MCP stdio servers as agentcore tools` after the `gate/` line. CLAUDE.md "Package layout": add a `**mcp/**` bullet in the same words. CHANGELOG: read the file's existing entry format and add an entry at the top for the new package, in that format.

- [ ] **Step 3: Gate and commit**

```bash
cd /Users/guygrigsby/projects/jess && go vet ./... && git add docs/adr/0004-mcp-adapter.md README.md CHANGELOG.md CLAUDE.md && git commit -m "docs: mcp adapter package and ADR 0004"
```

---

### Task 3: gyr cutover

**Files:**
- Delete: `internal/mcp/` (all files)
- Modify: `cmd/gyrd/main.go` (line 188 area, the `mcp.Tools` call)
- Modify: `go.mod` (jess version)
- Create: `docs/adr/0009-mcp-adapter-moved-to-jess.md`
- Modify: `docs/adr/README.md` if it indexes ADRs

**Interfaces:**
- Consumes: `jessmcp.Server`, `jessmcp.Tools` from Task 1.

- [ ] **Step 1: Point gyr at the local jess**

Until jess is pushed and tagged, gyr consumes it through a replace directive. Run:

```bash
cd /Users/guygrigsby/projects/gyr && go mod edit -replace github.com/guygrigsby/jess=../jess && go get github.com/guygrigsby/jess@v0.0.0 2>/dev/null; go mod tidy
```

If `go get` rejects `v0.0.0`, leave the existing `require` line as is; the `replace` wins.

- [ ] **Step 2: Write the failing build**

Delete the package and rewire the call:

```bash
cd /Users/guygrigsby/projects/gyr && git rm -rq internal/mcp
```

In `cmd/gyrd/main.go` replace the import `"github.com/guygrigsby/gyr/internal/mcp"` with `jessmcp "github.com/guygrigsby/jess/mcp"` and the call:

```go
	mcpTools, mcpCloser, err := jessmcp.Tools(ctx, mcpServers(cfg.MCP.Servers), log.Printf)
```

Add to `cmd/gyrd/main.go`:

```go
// mcpServers maps gyr's config allowlist to jess's server descriptions. gyr
// always namespaces tool names by server (Bare false) so two servers cannot
// collide in the model's tool list.
func mcpServers(in []config.MCPServer) []jessmcp.Server {
	out := make([]jessmcp.Server, 0, len(in))
	for _, s := range in {
		out = append(out, jessmcp.Server{Name: s.Name, Command: s.Command, Args: s.Args, Env: s.Env})
	}
	return out
}
```

Run: `cd /Users/guygrigsby/projects/gyr && go build ./... 2>&1 | head`
Expected: builds. If `config` is not already imported in `main.go`, add `"github.com/guygrigsby/gyr/internal/config"`.

- [ ] **Step 3: Write a test for the mapping**

Create `cmd/gyrd/mcp_test.go`:

```go
package main

import (
	"testing"

	"github.com/guygrigsby/gyr/internal/config"
)

func TestMCPServersKeepNamespacing(t *testing.T) {
	in := []config.MCPServer{{Name: "git", Command: "uvx", Args: []string{"mcp-server-git"}, Env: []string{"A=1"}}}
	out := mcpServers(in)
	if len(out) != 1 || out[0].Name != "git" || out[0].Command != "uvx" || len(out[0].Args) != 1 || out[0].Env[0] != "A=1" {
		t.Errorf("mapped = %+v", out)
	}
	if out[0].Bare {
		t.Error("gyr must namespace tool names by server")
	}
}
```

Run: `cd /Users/guygrigsby/projects/gyr && go test ./cmd/gyrd/ -run TestMCPServers -v 2>&1 | tail -5`
Expected: PASS.

- [ ] **Step 4: ADR 0009**

`docs/adr/0009-mcp-adapter-moved-to-jess.md`:

```markdown
# 9. MCP adapter moved to jess

Status: Accepted. Amends ADR 0002 (the adapter's contract is unchanged; its home is not).
Date: 2026-09-04

## Context

`internal/mcp` was needed by a second jess host (autophage) and could not be imported across modules. jess ADR 0004 moved it to `jess/mcp` with two additions gyr does not use: bare tool names and JSON result passthrough.

## Decision

Delete `internal/mcp`; import `jess/mcp`. gyr maps `[[mcp.servers]]` to `mcp.Server` with `Bare` false, so tool names stay namespaced by server.

## Consequences

- One adapter across gyr and autophage.
- A tool whose text result is valid JSON now reaches the model unwrapped rather than inside `{"output": ...}`. The built-in tools are unaffected; MCP tools returning JSON text look different to the model, which is an improvement.
- Until jess is published with `mcp/`, `go.mod` carries a local `replace`; drop it when the tag exists.
```

If `docs/adr/README.md` lists ADRs, add 0009 to it.

- [ ] **Step 5: Gate and commit**

Run: `cd /Users/guygrigsby/projects/gyr && make check 2>&1 | tail -12`
Expected: clean. If `make check` runs golangci-lint via `go run` and that takes long, let it.

```bash
cd /Users/guygrigsby/projects/gyr && git add -A && git commit -m "mcp: use jess/mcp, drop the internal copy"
```

`git add -A` is acceptable here because the deletion of `internal/mcp` must land with the rewiring; confirm `git status` shows nothing unexpected first.

---

## Self-review

Spec coverage: ADR 0002 in autophage asks for the extraction so autophage and gyr share the adapter (Task 1, Task 3), with the toolbox dialed by command (`Server{Command: "podman", Args: [...]}` needs nothing new) and bare tool names for the model (Task 1 `Bare`). JSON passthrough is required because agentcore tools return JSON. Docs in both repos (Tasks 2 and 4).

Type consistency: `Server` fields match between the type, the tests and gyr's mapping; `uniqueName` takes the `bare bool` in every call site; `dial`/`dialFunc`/`client` names are gyr's and unchanged.
