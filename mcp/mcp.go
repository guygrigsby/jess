package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	ac "github.com/voocel/agentcore"
)

// ErrServerGone is the sentinel a callTool error wraps when the underlying
// MCP server's connection has closed (the subprocess exited or the transport
// dropped). It is jess's own error value; no MCP SDK type crosses this
// boundary. Check it with errors.Is.
var ErrServerGone = errors.New("mcp: server connection closed")

// Server describes one stdio MCP server to launch. Command and Args are the
// launch line. The subprocess inherits the daemon's own environment plus Env
// ("K=V" pairs appended after it).
type Server struct {
	Name    string
	Command string
	Args    []string
	Env     []string
	// Bare exposes the server's tool names as-is (sanitized) instead of
	// prefixed with "<Name>__"; use it when the host runs a single server and
	// wants the model to see the tools' own names. Bare names are NOT
	// deduplicated against the host's own tools: Tools cannot see them, so a
	// bare "read" or "bash" can silently shadow a host tool of the same name.
	// Avoiding that collision is the host's responsibility.
	Bare bool
}

// toolDef is the boundary-local description of one MCP tool. It carries only
// what the adapter needs; SDK types stay on the far side of [client].
type toolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
	// ReadOnly carries the server's ReadOnlyHint annotation, when present, so
	// the adapted tool can report it to agentcore for concurrent scheduling.
	ReadOnly bool
}

// client is the minimal MCP surface the adapter depends on. The real
// implementation (client_sdk.go) is the only place that imports the MCP SDK;
// tests supply a fake.
//
// callTool's contract: the tool's own reported failure (MCP IsError) comes
// back as ordinary text with a nil error, since it is meant for the model to
// read and self-correct, not a Go error the host should react to. A non-nil
// error means the call itself failed (transport or protocol), and a
// connection-closed failure wraps ErrServerGone.
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
// an ac.Tool the agent understands. A server that fails to dial or list is
// logged via logf and SKIPPED, unless every server requested fails: then
// Tools returns an error naming each server and its failure, with no tools
// and a closer that is already safe to close. Partial failure (at least one
// server dials and lists successfully) still returns a nil error with the
// working servers' tools.
//
// ctx bounds only the dial, initialize and tools/list handshake for each
// server; the server subprocess and its session outlive ctx and keep running
// after Tools returns. The returned io.Closer is the only teardown: it shuts
// down every started client, and callers must call it when done with the
// tools.
func Tools(ctx context.Context, servers []Server, logf func(string, ...any)) ([]ac.Tool, io.Closer, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var (
		out    []ac.Tool
		closer = &multiCloser{}
		seen   = map[string]bool{}
		failed []string
	)
	for _, srv := range servers {
		c, err := dial(ctx, srv)
		if err != nil {
			logf("mcp: server %q: dial: %v (skipped)", srv.Name, err)
			failed = append(failed, fmt.Sprintf("%s: dial: %v", srv.Name, err))
			continue
		}
		defs, err := c.listTools(ctx)
		if err != nil {
			logf("mcp: server %q: list tools: %v (skipped)", srv.Name, err)
			_ = c.Close()
			failed = append(failed, fmt.Sprintf("%s: list tools: %v", srv.Name, err))
			continue
		}
		closer.add(c)
		names := make([]string, 0, len(defs))
		for _, def := range defs {
			name := uniqueName(srv.Name, def.Name, srv.Bare, seen)
			names = append(names, name)
			out = append(out, &mcpTool{
				name:        name,
				realName:    def.Name,
				description: describeTool(srv, def.Description),
				schema:      schemaOrDefault(def.InputSchema),
				client:      c,
				readOnly:    def.ReadOnly,
			})
		}
		logf("mcp: server %q tools: %v", srv.Name, names)
	}
	if len(servers) > 0 && len(failed) == len(servers) {
		_ = closer.Close()
		return nil, closer, fmt.Errorf("mcp: every server failed: %s", strings.Join(failed, "; "))
	}
	return out, closer, nil
}

// describeTool builds a tool's description: bare servers and empty
// descriptions pass the text through as-is; otherwise it is prefixed
// "[server] " so the model can tell which server a namespaced tool came from.
func describeTool(srv Server, desc string) string {
	if desc == "" || srv.Bare {
		return desc
	}
	return fmt.Sprintf("[%s] %s", srv.Name, desc)
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
//
// callTool's own IsError results are not Go errors (see the client doc
// comment): Execute returns them as ordinary output text prefixed "error: "
// so the model reads and can self-correct, and agentcore's consecutive-error
// breaker does not count them against the tool. A non-nil error from
// callTool means the call itself failed (transport or protocol failure, or
// the server connection closing, wrapped as ErrServerGone).
type mcpTool struct {
	name        string
	realName    string // the tool name to send over MCP (unprefixed)
	description string
	schema      map[string]any
	client      client
	readOnly    bool
}

func (t *mcpTool) Name() string           { return t.name }
func (t *mcpTool) Description() string    { return t.description }
func (t *mcpTool) Schema() map[string]any { return t.schema }

// ReadOnly reports the server's ReadOnlyHint annotation for this tool, so
// agentcore can run it concurrently with other tools instead of serializing
// every MCP call. args is unused: the hint is per-tool, not per-invocation.
func (t *mcpTool) ReadOnly(_ json.RawMessage) bool { return t.readOnly }

// Execute unmarshals the model's args and calls the MCP tool by its real
// name. A result whose first non-space byte is '{' or '[' and that is valid
// JSON passes through unwrapped; scalars (numbers, booleans, quoted strings)
// and plain text are wrapped as {"output": <result>}.
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
	if looksLikeJSONContainer(res) && json.Valid([]byte(res)) {
		return json.RawMessage(res), nil
	}
	return mustJSON(map[string]string{"output": res}), nil
}

// looksLikeJSONContainer reports whether s's first non-whitespace byte opens
// a JSON object or array. Scalars and quoted strings are also valid JSON but
// are not containers, so they keep the {"output": ...} envelope rather than
// passing through as a bare value the model would otherwise have to parse.
func looksLikeJSONContainer(s string) bool {
	for i := range len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
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
