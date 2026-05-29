package event

import (
	"sync"
	"testing"
	"time"
)

func TestStream_SendReceiveClose(t *testing.T) {
	s := NewStream(4)
	s.Send(Event{Kind: KindRunStart})
	s.Send(Event{Kind: KindRunEnd})
	s.Close()

	var kinds []EventKind
	for ev := range s.Events() { // range must terminate once Close ran
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 2 || kinds[0] != KindRunStart || kinds[1] != KindRunEnd {
		t.Fatalf("events = %v, want [run_start run_end]", kinds)
	}
}

func TestStream_SendAfterCloseIsNoop(t *testing.T) {
	s := NewStream(1)
	s.Close()
	s.Send(Event{Kind: KindError}) // must not panic
	s.Close()                      // double close must not panic
	if _, ok := <-s.Events(); ok {
		t.Error("closed stream should yield a closed channel")
	}
}

// Many producers, one consumer, then Close after producers finish. Run with
// -race to catch any data race on the channel or closed flag.
func TestStream_ConcurrentProducers(t *testing.T) {
	s := NewStream(8)

	var got int
	done := make(chan struct{})
	go func() {
		for range s.Events() {
			got++
		}
		close(done)
	}()

	const producers, each = 16, 50
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				s.Send(Event{Kind: KindMessageDelta})
			}
		}()
	}
	wg.Wait()
	s.Close()
	<-done

	if got != producers*each {
		t.Errorf("received %d events, want %d", got, producers*each)
	}
}

// Close must unblock a Send that is blocked on a full buffer (no consumer),
// rather than hang waiting for the write lock behind it.
func TestStream_CloseUnblocksBlockedSend(t *testing.T) {
	s := NewStream(1)
	s.Send(Event{Kind: KindRunStart}) // fill the buffer (cap 1)

	sent := make(chan struct{})
	go func() {
		s.Send(Event{Kind: KindMessageDelta}) // blocks: buffer full, no consumer
		close(sent)
	}()
	time.Sleep(50 * time.Millisecond) // let the producer block on the send

	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung behind a blocked Send")
	}
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked Send did not unblock after Close")
	}
}
