package model

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/message"
)

// fakeModel proves the interface is implementable and locks the method set.
type fakeModel struct{ chunks []Chunk }

func (f fakeModel) SupportsTools() bool { return true }
func (f fakeModel) Stream(_ context.Context, _ []message.Message, _ []ToolSpec) (<-chan Chunk, error) {
	ch := make(chan Chunk, len(f.chunks))
	for _, c := range f.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

var _ Model = fakeModel{}

func TestModel_StreamYieldsChunks(t *testing.T) {
	m := fakeModel{chunks: []Chunk{
		{Delta: "hi", DeltaKind: ""},
		{Done: true, Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"},
	}}
	ch, err := m.Stream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []Chunk
	for c := range ch {
		got = append(got, c)
	}
	if len(got) != 2 || got[0].Delta != "hi" || !got[1].Done || got[1].StopReason != "stop" {
		t.Fatalf("chunks = %+v", got)
	}
}
