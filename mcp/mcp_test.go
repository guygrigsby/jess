package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// fakeClient is a hermetic stand-in for the MCP SDK client: no subprocess, no
// SDK. It records the last callTool invocation so tests can assert it.
type fakeClient struct {
	defs    []toolDef
	listErr error
	result  string
	callErr error
	closed  bool
	gotName string
	gotArgs map[string]any
}

func (f *fakeClient) listTools(context.Context) ([]toolDef, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.defs, nil
}

func (f *fakeClient) callTool(_ context.Context, name string, args map[string]any) (string, error) {
	f.gotName, f.gotArgs = name, args
	if f.callErr != nil {
		return "", f.callErr
	}
	return f.result, nil
}

func (f *fakeClient) Close() error { f.closed = true; return nil }

// withDial swaps the package dialer to d and returns a func that restores the
// original. Callers defer the restore.
func withDial(d dialFunc) (restore func()) {
	old := dial
	dial = d
	return func() { dial = old }
}

// withDialMap swaps the package dialer to return clients[server.Name],
// erroring for any server not in the map. It returns the restore func from
// withDial.
func withDialMap(clients map[string]client) (restore func()) {
	return withDial(func(_ context.Context, srv Server) (client, error) {
		c, ok := clients[srv.Name]
		if !ok {
			return nil, errors.New("no fake for " + srv.Name)
		}
		return c, nil
	})
}

func TestToolsAdaptsNamespacedNamesAndSchema(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
	}
	fc := &fakeClient{defs: []toolDef{
		{Name: "read_file", Description: "Read a file", InputSchema: schema},
		{Name: "no_schema", Description: "no schema"},
	}}
	restore := withDialMap(map[string]client{"fs": fc})
	defer restore()

	tools, closer, err := Tools(context.Background(), []Server{{Name: "fs"}}, nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	defer func() { _ = closer.Close() }()

	if len(tools) != 2 {
		t.Fatalf("tool count: %d", len(tools))
	}
	if got := tools[0].Name(); got != "fs__read_file" {
		t.Fatalf("namespaced name: %q", got)
	}
	if got := tools[0].Description(); got != "[fs] Read a file" {
		t.Fatalf("description: %q", got)
	}
	if !reflect.DeepEqual(tools[0].Schema(), schema) {
		t.Fatalf("schema not passed through: %v", tools[0].Schema())
	}
	// Empty schema resolves to a minimal object.
	if got := tools[1].Schema(); !reflect.DeepEqual(got, map[string]any{"type": "object"}) {
		t.Fatalf("empty schema default: %v", got)
	}
}

func TestExecuteMarshalsArgsAndReturnsOutput(t *testing.T) {
	fc := &fakeClient{
		defs:   []toolDef{{Name: "echo", Description: "echo"}},
		result: "hello world",
	}
	restore := withDialMap(map[string]client{"srv": fc})
	defer restore()

	tools, closer, err := Tools(context.Background(), []Server{{Name: "srv"}}, nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	defer func() { _ = closer.Close() }()

	raw, err := tools[0].Execute(context.Background(), json.RawMessage(`{"msg":"hi","n":3}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// callTool gets the REAL (unprefixed) tool name and the decoded args.
	if fc.gotName != "echo" {
		t.Fatalf("callTool name: %q", fc.gotName)
	}
	if fc.gotArgs["msg"] != "hi" || fc.gotArgs["n"].(float64) != 3 {
		t.Fatalf("callTool args: %v", fc.gotArgs)
	}
	var out struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if out.Output != "hello world" {
		t.Fatalf("output: %q", out.Output)
	}
}

func TestListToolsErrorSkipsServerWithoutFailingOthers(t *testing.T) {
	bad := &fakeClient{listErr: errors.New("boom")}
	good := &fakeClient{defs: []toolDef{{Name: "ok", Description: "ok"}}}
	restore := withDialMap(map[string]client{"bad": bad, "good": good})
	defer restore()

	servers := []Server{{Name: "bad"}, {Name: "good"}}
	tools, closer, err := Tools(context.Background(), servers, nil)
	if err != nil {
		t.Fatalf("Tools must not fail when one server errors: %v", err)
	}
	defer func() { _ = closer.Close() }()

	if len(tools) != 1 || tools[0].Name() != "good__ok" {
		var names []string
		for _, tl := range tools {
			names = append(names, tl.Name())
		}
		t.Fatalf("bad server tools must be absent; got %v", names)
	}
	// A skipped server's client is closed immediately.
	if !bad.closed {
		t.Fatal("skipped server's client should be closed")
	}
}

func TestNamesAreSanitizedAndUnique(t *testing.T) {
	fc := &fakeClient{defs: []toolDef{
		{Name: "weird name!"}, // space + '!' sanitized to '_'
		{Name: "weird name?"}, // sanitizes to the SAME base, so must dedupe
	}}
	restore := withDialMap(map[string]client{"s.v": fc}) // server name also needs sanitizing
	defer restore()

	tools, closer, err := Tools(context.Background(), []Server{{Name: "s.v"}}, nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	defer func() { _ = closer.Close() }()

	seen := map[string]bool{}
	for _, tl := range tools {
		n := tl.Name()
		if !anthropicToolName.MatchString(n) {
			t.Fatalf("name %q violates Anthropic rule", n)
		}
		if seen[n] {
			t.Fatalf("duplicate name %q", n)
		}
		seen[n] = true
	}
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
}

func TestCloserClosesEveryStartedClient(t *testing.T) {
	a := &fakeClient{defs: []toolDef{{Name: "a"}}}
	b := &fakeClient{defs: []toolDef{{Name: "b"}}}
	restore := withDialMap(map[string]client{"a": a, "b": b})
	defer restore()

	_, closer, err := Tools(context.Background(), []Server{{Name: "a"}, {Name: "b"}}, nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !a.closed || !b.closed {
		t.Fatalf("both clients should close: a=%v b=%v", a.closed, b.closed)
	}
}

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
