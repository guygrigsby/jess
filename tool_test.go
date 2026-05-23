package jess

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// staticTool is a Tool whose behavior is fully controlled by the test.
// Keeps the runner tests free of business logic — every assertion is
// about dispatch semantics, not what real tools happen to do.
type staticTool struct {
	name   string
	desc   string
	schema string
	run    func(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

func (t *staticTool) Spec() ToolSpec {
	return ToolSpec{
		Name:             t.name,
		Description:      t.desc,
		ParametersSchema: json.RawMessage(t.schema),
	}
}

func (t *staticTool) Run(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	if t.run == nil {
		return ToolResult{Output: "ok"}, nil
	}
	return t.run(ctx, args)
}

func TestNewToolRunner_AcceptsEmpty(t *testing.T) {
	r, err := NewToolRunner()
	if err != nil {
		t.Fatalf("empty runner errored: %v", err)
	}
	if names := r.Names(); len(names) != 0 {
		t.Errorf("expected no registered names, got %v", names)
	}
	if specs := r.Specs(); len(specs) != 0 {
		t.Errorf("expected no specs, got %v", specs)
	}
}

func TestNewToolRunner_RejectsDuplicateName(t *testing.T) {
	a := &staticTool{name: "echo"}
	b := &staticTool{name: "echo"}
	_, err := NewToolRunner(a, b)
	if err == nil {
		t.Fatal("expected error on duplicate name")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate: %v", err)
	}
}

func TestNewToolRunner_RejectsEmptyName(t *testing.T) {
	_, err := NewToolRunner(&staticTool{name: ""})
	if err == nil {
		t.Fatal("expected error on empty Spec().Name")
	}
}

func TestNewToolRunner_RejectsNilTool(t *testing.T) {
	_, err := NewToolRunner(nil)
	if err == nil {
		t.Fatal("expected error on nil tool")
	}
}

func TestToolRunner_Run_DispatchesToRegisteredTool(t *testing.T) {
	gotArgs := ""
	tool := &staticTool{
		name: "echo",
		run: func(_ context.Context, args json.RawMessage) (ToolResult, error) {
			gotArgs = string(args)
			return ToolResult{Output: "ran"}, nil
		},
	}
	r, err := NewToolRunner(tool)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), ToolCall{
		ID:            "call-1",
		Name:          "echo",
		ArgumentsJSON: `{"msg":"hi"}`,
	})
	if err != nil {
		t.Fatalf("Run errored: %v", err)
	}
	if res.Output != "ran" {
		t.Errorf("Output=%q, want ran", res.Output)
	}
	if gotArgs != `{"msg":"hi"}` {
		t.Errorf("Tool saw args=%q, want %q", gotArgs, `{"msg":"hi"}`)
	}
}

func TestToolRunner_Run_UnknownName(t *testing.T) {
	r, err := NewToolRunner(&staticTool{name: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), ToolCall{Name: "missing"})
	if err == nil {
		t.Fatal("expected error on unknown name")
	}
	// Error should list registered names so the caller can route a
	// useful response back to the model.
	if !strings.Contains(err.Error(), "echo") {
		t.Errorf("error should list registered tools: %v", err)
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should mention the requested name: %v", err)
	}
}

func TestToolRunner_Run_PropagatesInfraError(t *testing.T) {
	sentinel := errors.New("backing service down")
	r, err := NewToolRunner(&staticTool{
		name: "fail",
		run: func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
			return ToolResult{}, sentinel
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := r.Run(context.Background(), ToolCall{Name: "fail"})
	if !errors.Is(runErr, sentinel) {
		t.Errorf("Run should propagate infra error, got %v", runErr)
	}
}

// Tool-domain errors (the tool ran but reports a problem) come back as
// IsError=true with a non-nil err return — that lets the harness route
// the message to the model instead of aborting the run.
func TestToolRunner_Run_ToolDomainErrorIsNotInfra(t *testing.T) {
	r, err := NewToolRunner(&staticTool{
		name: "soft-fail",
		run: func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
			return ToolResult{Output: "bad input", IsError: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, runErr := r.Run(context.Background(), ToolCall{Name: "soft-fail"})
	if runErr != nil {
		t.Errorf("tool-domain error should NOT surface as infra err, got %v", runErr)
	}
	if !res.IsError {
		t.Errorf("IsError=false, want true")
	}
	if res.Output != "bad input" {
		t.Errorf("Output=%q, want bad input", res.Output)
	}
}

func TestToolRunner_Specs_StableSorted(t *testing.T) {
	r, err := NewToolRunner(
		&staticTool{name: "zebra", desc: "Z"},
		&staticTool{name: "alpha", desc: "A"},
		&staticTool{name: "mike", desc: "M"},
	)
	if err != nil {
		t.Fatal(err)
	}
	specs := r.Specs()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	want := []string{"alpha", "mike", "zebra"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("Specs not sorted: got %v, want %v", names, want)
	}
}

func TestToolRunner_Register_RejectsDuplicate(t *testing.T) {
	r, err := NewToolRunner(&staticTool{name: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&staticTool{name: "echo"}); err == nil {
		t.Fatal("expected error registering duplicate")
	}
}

func TestToolRunner_Unregister_AllowsReRegister(t *testing.T) {
	r, err := NewToolRunner(&staticTool{name: "echo", desc: "first"})
	if err != nil {
		t.Fatal(err)
	}
	r.Unregister("echo")
	if err := r.Register(&staticTool{name: "echo", desc: "second"}); err != nil {
		t.Errorf("re-register after unregister should succeed: %v", err)
	}
	if r.Specs()[0].Description != "second" {
		t.Errorf("expected new tool to win, got %q", r.Specs()[0].Description)
	}
}

// Race regression: Register/Unregister/Run must be safe to call
// concurrently. The harness will dispatch multiple tool calls in
// parallel within a single iteration; nothing here should panic
// or deadlock under -race.
func TestToolRunner_ConcurrentRunDispatchSafe(t *testing.T) {
	r, err := NewToolRunner(&staticTool{
		name: "echo",
		run: func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
			return ToolResult{Output: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Run(context.Background(), ToolCall{Name: "echo"}); err != nil {
				t.Errorf("concurrent Run errored: %v", err)
			}
		}()
	}
	wg.Wait()
}
