//go:build windows

package handoff

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	winapi "golang.org/x/sys/windows"
)

const (
	pipePrefix = `\\.\pipe\sing-box-drover-handoff-`
	// maxFrameBytes bounds one protocol frame. The protocol only carries a kind,
	// a short reason and a token, so this is generous.
	maxFrameBytes = 4 << 10
	// maxReasonBytes bounds the reason a message carries. A reason is user-facing
	// text and can be a whole core check output, so it is trimmed instead of making
	// its frame unsendable.
	maxReasonBytes = 1 << 10
	// connectInterval paces the retry while the pipe server is still busy.
	connectInterval = 20 * time.Millisecond
)

// DefaultTransport returns the transport used for controller handoffs.
//
// The pipe is created with an explicit DACL for the account running the parent,
// so an elevated replacement of that same account can connect and a process of
// any other account cannot. Both ends authenticate the peer's process identity
// and exchange the token before the protocol starts.
func DefaultTransport() (Transport, error) { return pipeTransport{}, nil }

type pipeTransport struct{}

func (pipeTransport) Listen() (Listener, error) {
	name, err := randomHex("handoff identifier", 32)
	if err != nil {
		return nil, err
	}
	token, err := randomHex("handoff token", 32)
	if err != nil {
		return nil, err
	}
	descriptor, err := localUserSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	address := pipePrefix + name
	namePtr, err := winapi.UTF16PtrFromString(address)
	if err != nil {
		return nil, fmt.Errorf("encode handoff pipe name: %w", err)
	}
	attributes := winapi.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(winapi.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	// Abort monitoring and takeover confirmation use the same pipe concurrently.
	handle, err := winapi.CreateNamedPipe(namePtr, winapi.PIPE_ACCESS_DUPLEX|winapi.FILE_FLAG_OVERLAPPED, 0, 1, maxFrameBytes, maxFrameBytes, 0, &attributes)
	if err != nil {
		return nil, fmt.Errorf("create handoff pipe: %w", err)
	}
	return &pipeListener{address: address, token: token, handle: handle}, nil
}

func randomHex(what string, size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate %s: %w", what, err)
	}
	return hex.EncodeToString(buffer), nil
}

// localUserSecurityDescriptor grants full access to the local account running
// this process, the local system and the administrators group. Everything else
// is denied, so a process of another account cannot connect to the pipe even
// when it knows the name.
func localUserSecurityDescriptor() (*winapi.SECURITY_DESCRIPTOR, error) {
	sddl, err := localUserSDDL()
	if err != nil {
		return nil, err
	}
	descriptor, err := winapi.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build the handoff pipe ACL: %w", err)
	}
	return descriptor, nil
}

// localUserSDDL is the descriptor string the pipe is created with: a protected
// DACL that grants access to the local system, the administrators group and the
// account running this process.
func localUserSDDL() (string, error) {
	user, err := winapi.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("resolve the current user: %w", err)
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + user.User.Sid.String() + ")", nil
}

type pipeListener struct {
	address string
	token   string
	// mu guards handle: Accept moves it to the connection and Close may run
	// concurrently when a phase deadline ends the wait.
	mu       sync.Mutex
	handle   winapi.Handle
	accepted bool
}

func (l *pipeListener) Address() string { return l.address }
func (l *pipeListener) Token() string   { return l.token }

func (l *pipeListener) Accept(ctx context.Context, expectedPID int) (Conn, error) {
	handle := l.take()
	if handle == 0 {
		return nil, errors.New("the handoff pipe is closed")
	}
	event, err := winapi.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = winapi.CloseHandle(handle)
		return nil, err
	}
	defer winapi.CloseHandle(event)
	overlap := &winapi.Overlapped{HEvent: event}
	err = winapi.ConnectNamedPipe(handle, overlap)
	if errors.Is(err, winapi.ERROR_IO_PENDING) {
		connected := make(chan error, 1)
		go func() {
			var transferred uint32
			connected <- winapi.GetOverlappedResult(handle, overlap, &transferred, true)
		}()
		select {
		case err = <-connected:
		case <-ctx.Done():
			_ = winapi.CancelIoEx(handle, overlap)
			<-connected
			err = ctx.Err()
		}
	}
	if err != nil && !errors.Is(err, winapi.ERROR_PIPE_CONNECTED) {
		_ = winapi.CloseHandle(handle)
		return nil, fmt.Errorf("accept the handoff connection: %w", err)
	}
	conn := newPipeConn(handle, l.token)
	if err := conn.verifyClient(expectedPID); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.authenticate(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (l *pipeListener) take() winapi.Handle {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.accepted {
		return 0
	}
	l.accepted = true
	handle := l.handle
	l.handle = 0
	return handle
}

func (l *pipeListener) Close() error {
	l.mu.Lock()
	handle := l.handle
	l.handle = 0
	l.accepted = true
	l.mu.Unlock()
	if handle != 0 {
		_ = winapi.CloseHandle(handle)
	}
	return nil
}

// Connect opens the parent's handoff pipe. serverPID is the process the caller
// was told to connect to: the connection is rejected unless the pipe belongs to
// exactly that process.
func Connect(ctx context.Context, address, token string, serverPID int) (Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	namePtr, err := winapi.UTF16PtrFromString(address)
	if err != nil {
		return nil, fmt.Errorf("encode handoff pipe name: %w", err)
	}
	for {
		handle, err := winapi.CreateFile(namePtr, winapi.GENERIC_READ|winapi.GENERIC_WRITE, 0, nil, winapi.OPEN_EXISTING, winapi.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			conn := newPipeConn(handle, token)
			if err := conn.verifyServer(serverPID); err != nil {
				conn.Close()
				return nil, err
			}
			if err := conn.Send(Message{Kind: KindAuth, Token: token}); err != nil {
				conn.Close()
				return nil, fmt.Errorf("authenticate the handoff connection: %w", err)
			}
			return conn, nil
		}
		if !errors.Is(err, winapi.ERROR_PIPE_BUSY) {
			return nil, fmt.Errorf("connect to the previous instance: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(connectInterval):
		}
	}
}

type pipeConn struct {
	// writeMu serializes frames: the connection is used by one goroutine at a
	// time, but a rollback may send while a phase deadline closes the pipe.
	writeMu sync.Mutex
	// os.File uses the runtime poller for overlapped handles, allowing full-duplex
	// I/O and cancelling pending operations when Close is called.
	file  *os.File
	token string
}

func newPipeConn(handle winapi.Handle, token string) *pipeConn {
	return &pipeConn{file: os.NewFile(uintptr(handle), "handoff pipe"), token: token}
}

func (c *pipeConn) fileHandle() winapi.Handle {
	return winapi.Handle(c.file.Fd())
}

func (c *pipeConn) verifyClient(expectedPID int) error {
	var pid uint32
	if err := winapi.GetNamedPipeClientProcessId(c.fileHandle(), &pid); err != nil {
		return fmt.Errorf("identify the handoff client: %w", err)
	}
	if int(pid) != expectedPID {
		return fmt.Errorf("the handoff pipe was connected by process %d, expected the launched replacement %d", pid, expectedPID)
	}
	return nil
}

func (c *pipeConn) verifyServer(expectedPID int) error {
	if expectedPID <= 0 {
		return errors.New("the handoff pipe owner is unknown")
	}
	var pid uint32
	if err := winapi.GetNamedPipeServerProcessId(c.fileHandle(), &pid); err != nil {
		return fmt.Errorf("identify the handoff server: %w", err)
	}
	if int(pid) != expectedPID {
		return fmt.Errorf("the handoff pipe belongs to process %d, expected %d", pid, expectedPID)
	}
	return nil
}

// authenticate consumes the peer's opening frame and checks the shared token.
func (c *pipeConn) authenticate(ctx context.Context) error {
	message, err := c.Receive(ctx)
	if err != nil {
		return fmt.Errorf("read the handoff authentication: %w", err)
	}
	if message.Kind != KindAuth || message.Token != c.token {
		return errors.New("the handoff connection did not authenticate")
	}
	return nil
}

// trimReason shortens a reason to what a frame can carry. It never cuts a rune in
// half: a reason is shown to a user.
func trimReason(reason string) string {
	if len(reason) <= maxReasonBytes {
		return reason
	}
	return strings.ToValidUTF8(reason[:maxReasonBytes], "") + "..."
}

func (c *pipeConn) Send(message Message) error {
	message.Reason = trimReason(message.Reason)
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode handoff message: %w", err)
	}
	if len(payload) > maxFrameBytes {
		return fmt.Errorf("handoff message of %d bytes is too large", len(payload))
	}
	frame := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	return c.write(frame)
}

func (c *pipeConn) write(frame []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	written, err := c.file.Write(frame)
	if err != nil {
		return err
	}
	if written != len(frame) {
		return io.ErrShortWrite
	}
	return nil
}

// Receive waits for the next message. It must not be called concurrently with
// itself: a read that a deadline abandoned stays blocked until the pipe closes,
// which every failure path does.
func (c *pipeConn) Receive(ctx context.Context) (Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type result struct {
		message Message
		err     error
	}
	done := make(chan result, 1)
	go func() {
		message, err := c.read()
		done <- result{message: message, err: err}
	}()
	select {
	case received := <-done:
		return received.message, received.err
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

func (c *pipeConn) read() (Message, error) {
	var header [4]byte
	if err := c.readFull(header[:]); err != nil {
		return Message{}, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > maxFrameBytes {
		return Message{}, fmt.Errorf("handoff frame of %d bytes is invalid", size)
	}
	payload := make([]byte, size)
	if err := c.readFull(payload); err != nil {
		return Message{}, err
	}
	var message Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return Message{}, fmt.Errorf("decode handoff message: %w", err)
	}
	return message, nil
}

func (c *pipeConn) readFull(buffer []byte) error {
	_, err := io.ReadFull(c.file, buffer)
	return err
}

func (c *pipeConn) Close() error {
	err := c.file.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}
