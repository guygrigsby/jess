package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	ac "github.com/voocel/agentcore"
)

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

// toolDef is the boundary-local description of one MCP tool. It carries only
// what the adapter needs; SDK types stay on the far side of [client].
type toolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// client is the minimal MCP surface the adapter depends on. The real
// implementation (client_sdk.go) is the only place that imports the MCP SDK;
// tests supply a fake.
type client interface {
	listTools(ctx context.Context) ([]toolDef, error)
	callTool(ctx context.Context, name string, args map[string]any) (string, error)
	io.Closer
}

// dialFunc opens a client for one server. Swapped in tests to avoid subprocesses.
type dialFunc func(ctx context.Context, srv Server) (client, error)

// dial is the production dialer (the real stdio SDK client).
var dial dialFunc = dialStdio

// anthropicToolName bounds a tool name to Anthropic's rule: 1..64 of
// [a-zA-Z0-9_-]. Adapter names are sanitized/truncated to match.
var anthropicToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Tools dials every allowlisted server, lists its tools and adapts each into
// an ac.Tool the agent understands. A server that fails to dial, initialize or
// list is logged via logf and SKIPPED; it never fails the whole call. The
// returned io.Closer shuts down every started client.
func Tools(ctx context.Context, servers []Server, logf func(string, ...any)) ([]ac.Tool, io.Closer, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var (
		out    []ac.Tool
		closer = &multiCloser{}
		seen   = map[string]bool{}
	)
	for _, srv := range servers {
		c, err := dial(ctx, srv)
		if err != nil {
			logf("mcp: server %q: dial: %v (skipped)", srv.Name, err)
			continue
		}
		defs, err := c.listTools(ctx)
		if err != nil {
			logf("mcp: server %q: list tools: %v (skipped)", srv.Name, err)
			_ = c.Close()
			continue
		}
		closer.add(c)
		for _, def := range defs {
			name := uniqueName(srv.Name, def.Name, srv.Bare, seen)
			out = append(out, &mcpTool{
				name:        name,
				realName:    def.Name,
				description: fmt.Sprintf("[%s] %s", srv.Name, def.Description),
				schema:      schemaOrDefault(def.InputSchema),
				client:      c,
			})
		}
		logf("mcp: server %q: %d tools", srv.Name, len(defs))
	}
	return out, closer, nil
}

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
		// Truncate the base so name+suffix still fits 64 chars.
		if len(base)+len(suffix) > 64 {
			base = base[:64-len(suffix)]
		}
		name = base + suffix
	}
	seen[name] = true
	return name
}

// sanitize maps any string to a valid Anthropic tool name: invalid runes
// become '_', the result is truncated to 64 and an empty result becomes "_".
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		out = "_"
	}
	return out
}

// schemaOrDefault returns s, or a minimal object schema when s is empty.
func schemaOrDefault(s map[string]any) map[string]any {
	if len(s) == 0 {
		return map[string]any{"type": "object"}
	}
	return s
}

// mcpTool adapts one MCP tool to ac.Tool. It is NON-safe by design: no Safe()
// method, so the jess gate confirm-gates and ledgers every Execute.
type mcpTool struct {
	name        string
	realName    string // the tool name to send over MCP (unprefixed)
	description string
	schema      map[string]any
	client      client
}

func (t *mcpTool) Name() string           { return t.name }
func (t *mcpTool) Description() string    { return t.description }
func (t *mcpTool) Schema() map[string]any { return t.schema }

// Execute unmarshals the model's args and calls the MCP tool by its real
// name. A result that is already valid JSON passes through unwrapped; plain
// text is wrapped as {"output": <result>}.
func (t *mcpTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	args := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("mcp %q: bad arguments: %w", t.name, err)
		}
	}
	res, err := t.client.callTool(ctx, t.realName, args)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: %w", t.name, err)
	}
	if json.Valid([]byte(res)) {
		return json.RawMessage(res), nil
	}
	return mustJSON(map[string]string{"output": res}), nil
}

// mustJSON marshals v, ignoring the (impossible for these shapes) error.
func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// multiCloser closes every added client; it accumulates close errors.
type multiCloser struct{ cs []io.Closer }

func (m *multiCloser) add(c io.Closer) { m.cs = append(m.cs, c) }

func (m *multiCloser) Close() error {
	var errs []error
	for _, c := range m.cs {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return fmt.Errorf("mcp close: %s", strings.Join(msgs, "; "))
}
