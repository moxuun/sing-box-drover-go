//go:build windows

package handoff

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// handoffDeadline is the budget a pipe test gives one phase. A real handoff uses
// much shorter budgets, so a test that needs this long is already failing.
const handoffDeadline = 10 * time.Second

type acceptResult struct {
	conn Conn
	err  error
}

func listenForTest(t *testing.T) Listener {
	t.Helper()
	transport, err := DefaultTransport()
	if err != nil {
		t.Fatalf("DefaultTransport() error = %v", err)
	}
	listener, err := transport.Listen()
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// acceptInBackground accepts the connection the test makes to itself. The peer
// is this process, so the identity check is satisfied by the current pid.
func acceptInBackground(listener Listener, expectedPID int) <-chan acceptResult {
	accepted := make(chan acceptResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), handoffDeadline)
		defer cancel()
		conn, err := listener.Accept(ctx, expectedPID)
		accepted <- acceptResult{conn: conn, err: err}
	}()
	return accepted
}

func connectForTest(t *testing.T, listener Listener, token string) Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), handoffDeadline)
	t.Cleanup(cancel)
	conn, err := Connect(ctx, listener.Address(), token, os.Getpid())
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestPipeTransportCarriesTheHandoffProtocol(t *testing.T) {
	listener := listenForTest(t)
	accepted := acceptInBackground(listener, os.Getpid())
	child := connectForTest(t, listener, listener.Token())

	ctx, cancel := context.WithTimeout(context.Background(), handoffDeadline)
	defer cancel()
	var parent Conn
	select {
	case result := <-accepted:
		if result.err != nil {
			t.Fatalf("Accept() error = %v", result.err)
		}
		parent = result.conn
	case <-ctx.Done():
		t.Fatal("Accept() did not return")
	}
	defer parent.Close()

	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("child Send() error = %v", err)
	}
	message, err := parent.Receive(ctx)
	if err != nil {
		t.Fatalf("parent Receive() error = %v", err)
	}
	if message.Kind != KindReady {
		t.Fatalf("parent received %q, want %q", message.Kind, KindReady)
	}
	if err := parent.Send(Message{Kind: KindProceed}); err != nil {
		t.Fatalf("parent Send() error = %v", err)
	}
	message, err = child.Receive(ctx)
	if err != nil {
		t.Fatalf("child Receive() error = %v", err)
	}
	if message.Kind != KindProceed {
		t.Fatalf("child received %q, want %q", message.Kind, KindProceed)
	}
}

func TestPipeTransportRejectsAnUnauthenticatedClient(t *testing.T) {
	listener := listenForTest(t)
	accepted := acceptInBackground(listener, os.Getpid())
	// The client connects and sends a token the parent did not hand out.
	child := connectForTest(t, listener, "not-the-token")

	ctx, cancel := context.WithTimeout(context.Background(), handoffDeadline)
	defer cancel()
	select {
	case result := <-accepted:
		if result.err == nil {
			result.conn.Close()
			t.Fatal("Accept() accepted a client that did not authenticate")
		}
	case <-ctx.Done():
		t.Fatal("Accept() did not reject the client")
	}
	if _, err := child.Receive(ctx); err == nil {
		t.Fatal("child kept a connection the parent rejected")
	}
}

func TestPipeTransportRejectsAConnectionToAnotherProcess(t *testing.T) {
	listener := listenForTest(t)
	// The parent is told to expect a different process than the one connecting.
	accepted := acceptInBackground(listener, os.Getpid()+1)
	connectForTest(t, listener, listener.Token())

	ctx, cancel := context.WithTimeout(context.Background(), handoffDeadline)
	defer cancel()
	select {
	case result := <-accepted:
		if result.conn != nil {
			result.conn.Close()
		}
		if result.err == nil {
			t.Fatal("Accept() accepted a connection from an unexpected process")
		}
	case <-ctx.Done():
		t.Fatal("Accept() did not reject the connection")
	}
}

func TestLocalUserSecurityDescriptorDeniesOtherAccounts(t *testing.T) {
	sddl, err := localUserSDDL()
	if err != nil {
		t.Fatalf("localUserSDDL() error = %v", err)
	}
	// The pipe has to be reachable by the account running the controller (and by
	// an elevated replacement of that same account) and by nobody else.
	if !strings.HasPrefix(sddl, "D:P") {
		t.Fatalf("descriptor %q is not a protected DACL", sddl)
	}
	for _, want := range []string{"(A;;GA;;;SY)", "(A;;GA;;;BA)"} {
		if !strings.Contains(sddl, want) {
			t.Fatalf("descriptor %q does not grant %q", sddl, want)
		}
	}
	for _, unwanted := range []string{";;;WD)", ";;;AN)", ";;;BU)"} {
		if strings.Contains(sddl, unwanted) {
			t.Fatalf("descriptor %q grants access to every account", sddl)
		}
	}
}
