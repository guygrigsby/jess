package acl

import (
	"context"
	"errors"
	"testing"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	ac "github.com/voocel/agentcore"
)

func TestToolSpecFromAC(t *testing.T) {
	in := ac.ToolSpec{Name: "search", Description: "d", Parameters: map[string]any{"type": "object"}}
	got := toolSpecFromAC(in)
	if got.Name != "search" || got.Description != "d" || got.Schema["type"] != "object" {
		t.Fatalf("got %+v", got)
	}
}

func TestToolSpecFromAC_NonMapParameters(t *testing.T) {
	got := toolSpecFromAC(ac.ToolSpec{Name: "x", Parameters: "not-a-map"})
	if got.Schema != nil {
		t.Errorf("non-map Parameters should yield nil Schema, got %v", got.Schema)
	}
}

func TestDeltaEventType(t *testing.T) {
	tests := []struct {
		in   event.DeltaKind
		want ac.StreamEventType
	}{
		{event.DeltaText, ac.StreamEventTextDelta},
		{event.DeltaThinking, ac.StreamEventThinkingDelta},
		{event.DeltaToolCall, ac.StreamEventToolCallDelta},
	}
	for _, tt := range tests {
		if got := deltaEventType(tt.in); got != tt.want {
			t.Errorf("deltaEventType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAssistantMessageToAC(t *testing.T) {
	jm := message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "hi"}}}
	got := assistantMessageToAC(jm, model.Usage{Input: 1, Output: 2, TotalTokens: 3}, "stop")
	if got.Role != ac.RoleAssistant || len(got.Content) != 1 || got.Content[0].Text != "hi" {
		t.Fatalf("message = %+v", got)
	}
	if got.StopReason != ac.StopReason("stop") || got.Usage == nil || got.Usage.TotalTokens != 3 {
		t.Errorf("stop/usage = %v / %+v", got.StopReason, got.Usage)
	}
}

// scriptedModel is a model.Model that emits a fixed Chunk script.
type scriptedModel struct{ chunks []model.Chunk }

func (scriptedModel) SupportsTools() bool { return true }
func (m scriptedModel) Stream(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (<-chan model.Chunk, error) {
	ch := make(chan model.Chunk)
	go func() {
		defer close(ch)
		for _, c := range m.chunks {
			select {
			case <-ctx.Done():
				return
			case ch <- c:
			}
		}
	}()
	return ch, nil
}

func TestStreamAdapter_GenerateStreamMapsChunks(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{
		{Delta: "he", DeltaKind: event.DeltaText},
		{Delta: "llo", DeltaKind: event.DeltaText},
		{Done: true, Message: message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "hello"}}}, StopReason: "stop"},
	}}
	var cm ac.ChatModel = ToAC(m)
	stream, err := cm.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var types []ac.StreamEventType
	var final ac.Message
	for ev := range stream {
		types = append(types, ev.Type)
		if ev.Type == ac.StreamEventDone {
			final = ev.Message
		}
	}
	if len(types) != 3 || types[0] != ac.StreamEventTextDelta || types[2] != ac.StreamEventDone {
		t.Fatalf("event types = %v", types)
	}
	if final.Content[0].Text != "hello" || final.StopReason != ac.StopReason("stop") {
		t.Errorf("final = %+v", final)
	}
}

func TestStreamAdapter_GenerateDrainsToFinal(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{
		{Delta: "x", DeltaKind: event.DeltaText},
		{Done: true, Message: message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "done"}}}, StopReason: "stop"},
	}}
	resp, err := ToAC(m).Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content[0].Text != "done" {
		t.Errorf("final message = %+v", resp.Message)
	}
}

func TestStreamAdapter_ErrChunkBecomesError(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{{Err: errors.New("boom")}}}
	_, err := ToAC(m).Generate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("want error from Err chunk")
	}
}

func TestStreamAdapter_ContextCancelStops(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{{Delta: "a", DeltaKind: event.DeltaText}, {Delta: "b", DeltaKind: event.DeltaText}}}
	ctx, cancel := context.WithCancel(context.Background())
	stream, _ := ToAC(m).GenerateStream(ctx, nil, nil)
	<-stream // first event
	cancel()
	for range stream { // must close without hanging
	}
}

// fakeChatModel is an agentcore.ChatModel emitting a fixed StreamEvent script.
type fakeChatModel struct{ events []ac.StreamEvent }

func (fakeChatModel) SupportsTools() bool { return true }
func (m fakeChatModel) GenerateStream(ctx context.Context, _ []ac.Message, _ []ac.ToolSpec, _ ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	ch := make(chan ac.StreamEvent, len(m.events))
	for _, e := range m.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}
func (m fakeChatModel) Generate(context.Context, []ac.Message, []ac.ToolSpec, ...ac.CallOption) (*ac.LLMResponse, error) {
	return &ac.LLMResponse{}, nil
}

func TestNativeModel_ToACUnwrapsToUnderlying(t *testing.T) {
	// Use a pointer so the interface value is comparable (slice-bearing structs
	// are not; comparing them directly panics at runtime).
	cm := &fakeChatModel{}
	nm := newNativeModel(cm)
	if ToAC(nm) != ac.ChatModel(cm) {
		t.Error("ToAC(nativeModel) must return the underlying ChatModel (passthrough)")
	}
}

func TestNativeModel_StreamBridgesToChunks(t *testing.T) {
	cm := fakeChatModel{events: []ac.StreamEvent{
		{Type: ac.StreamEventTextDelta, Delta: "hi"},
		{Type: ac.StreamEventDone, Message: ac.Message{Role: ac.RoleAssistant, Content: []ac.ContentBlock{ac.TextBlock("hi")}, Usage: &ac.Usage{Input: 1, Output: 2, TotalTokens: 3}}, StopReason: ac.StopReason("stop")},
	}}
	nm := newNativeModel(cm)
	ch, err := nm.Stream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []model.Chunk
	for c := range ch {
		got = append(got, c)
	}
	if len(got) != 2 || got[0].Delta != "hi" || got[0].DeltaKind != event.DeltaText {
		t.Fatalf("chunks = %+v", got)
	}
	if !got[1].Done || got[1].Message.Text() != "hi" || got[1].StopReason != "stop" {
		t.Errorf("final chunk = %+v", got[1])
	}
	if got[1].Usage.TotalTokens != 3 || got[1].Usage.Input != 1 {
		t.Errorf("usage not propagated: %+v", got[1].Usage)
	}
}

// blockingChatModel emits one delta then blocks until ctx is cancelled, never
// sending a Done. It exercises nativeModel.Stream's cancellation path.
type blockingChatModel struct{}

func (blockingChatModel) SupportsTools() bool { return true }
func (blockingChatModel) Generate(context.Context, []ac.Message, []ac.ToolSpec, ...ac.CallOption) (*ac.LLMResponse, error) {
	return &ac.LLMResponse{}, nil
}
func (blockingChatModel) GenerateStream(ctx context.Context, _ []ac.Message, _ []ac.ToolSpec, _ ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	ch := make(chan ac.StreamEvent)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			return
		case ch <- ac.StreamEvent{Type: ac.StreamEventTextDelta, Delta: "a"}:
		}
		<-ctx.Done() // block (no Done event) until cancelled
	}()
	return ch, nil
}

func TestNativeModel_StreamContextCancelStops(t *testing.T) {
	nm := newNativeModel(blockingChatModel{})
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := nm.Stream(ctx, nil, nil)
	<-ch // first chunk
	cancel()
	for range ch { // must close without hanging
	}
}

func TestStreamAdapter_NoDoneChunkIsError(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{{Delta: "x", DeltaKind: event.DeltaText}}} // no Done/Err
	_, err := ToAC(m).Generate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("want error when stream ends without a done chunk")
	}
}

// captureModel records the resolved MaxTokens from the CallOptions it receives.
type captureModel struct{ genMax, streamMax int }

func (m *captureModel) SupportsTools() bool { return true }
func (m *captureModel) Generate(_ context.Context, _ []ac.Message, _ []ac.ToolSpec, opts ...ac.CallOption) (*ac.LLMResponse, error) {
	var cc ac.CallConfig
	for _, o := range opts {
		o(&cc)
	}
	m.genMax = cc.MaxTokens
	return &ac.LLMResponse{Message: ac.Message{Role: ac.RoleAssistant}}, nil
}
func (m *captureModel) GenerateStream(_ context.Context, _ []ac.Message, _ []ac.ToolSpec, opts ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	var cc ac.CallConfig
	for _, o := range opts {
		o(&cc)
	}
	m.streamMax = cc.MaxTokens
	ch := make(chan ac.StreamEvent)
	close(ch)
	return ch, nil
}

func TestCappedChatModel_AppendsMaxTokens(t *testing.T) {
	cap := &captureModel{}
	capped := cappedChatModel{cm: cap, max: 99}
	if _, err := capped.Generate(context.Background(), nil, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := capped.GenerateStream(context.Background(), nil, nil); err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if cap.genMax != 99 {
		t.Errorf("Generate MaxTokens = %d, want 99", cap.genMax)
	}
	if cap.streamMax != 99 {
		t.Errorf("GenerateStream MaxTokens = %d, want 99", cap.streamMax)
	}
}
