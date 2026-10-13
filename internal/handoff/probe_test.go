package handoff

import (
	"context"
	"testing"
	"time"
)

func TestProbeHandover(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()
	events := &recorder{}
	process := &stubProcess{terminated: true}
	done := make(chan error, 1)
	go func() { done <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()
	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	m, err := child.Receive(context.Background())
	t.Logf("child got %q err=%v", m.Kind, err)
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Logf("RunParent returned early: %v (events %v)", err, events.list())
	default:
		t.Log("RunParent still waiting")
	}
	if err := child.Send(Message{Kind: KindTakeover}); err != nil {
		t.Logf("send takeover failed: %v", err)
	} else {
		t.Log("send takeover ok")
	}
	select {
	case err := <-done:
		t.Logf("RunParent returned: %v (events %v)", err, events.list())
	case <-time.After(time.Second):
		t.Log("RunParent never returned")
	}
}
