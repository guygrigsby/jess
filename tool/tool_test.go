package tool

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeTool exercises the Tool contract and locks the method set.
type fakeTool struct{}

func (fakeTool) Name() string           { return "echo" }
func (fakeTool) Description() string    { return "echoes its args" }
func (fakeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (fakeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

// Compile-time assertion that fakeTool satisfies Tool.
var _ Tool = fakeTool{}

func TestTool_Execute(t *testing.T) {
	var tl Tool = fakeTool{}
	got, err := tl.Execute(context.Background(), json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(got) != `{"x":1}` {
		t.Errorf("Execute echoed %s, want {\"x\":1}", got)
	}
	if tl.Name() != "echo" {
		t.Errorf("Name() = %q, want echo", tl.Name())
	}
}
