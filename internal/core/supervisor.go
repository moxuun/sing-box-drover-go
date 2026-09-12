package core

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"sing-box-drover/internal/logging"
)

const MaxCapturedOutput = 8 << 10

const startupGracePeriod = 750 * time.Millisecond
const configCheckTimeout = 30 * time.Second

type State int

const (
	StateStopped State = iota
	StateStarting
	StateRunning
	StateStopping
	StateFailed
)

type EventKind int

const (
	EventState EventKind = iota
	EventError
)

type Event struct {
	Kind    EventKind
	State   State
	Message string
}

type boundedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= MaxCapturedOutput {
		b.data = append(b.data[:0], p[len(p)-MaxCapturedOutput:]...)
		return len(p), nil
	}
	if overflow := len(b.data) + len(p) - MaxCapturedOutput; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}

type Supervisor struct {
	mu            sync.Mutex
	exePath       string
	logger        *logging.Logger
	cmd           *exec.Cmd
	process       *os.Process
	job           processJob
	state         State
	output        *boundedBuffer
	done          chan struct{}
	gen           uint64
	stopRequested bool
	handler       func(Event)
}

func NewSupervisor(exePath string, logger *logging.Logger) *Supervisor {
	return &Supervisor{exePath: exePath, logger: logger, state: StateStopped}
}

func (s *Supervisor) SetHandler(handler func(Event)) {
	s.mu.Lock()
	s.handler = handler
	s.mu.Unlock()
}

func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Supervisor) Running() bool {
	state := s.State()
	return state == StateStarting || state == StateRunning
}

func (s *Supervisor) LastOutput() string {
	s.mu.Lock()
	output := s.output
	s.mu.Unlock()
	if output == nil {
		return ""
	}
	return output.String()
}

func (s *Supervisor) notify(event Event) {
	s.mu.Lock()
	handler := s.handler
	s.mu.Unlock()
	if handler != nil {
		handler(event)
	}
}

// Check asks the configured sing-box executable to validate the exact runtime
// JSON without disturbing the currently managed process.
func (s *Supervisor) Check(configJSON string) error {
	if strings.TrimSpace(s.exePath) == "" {
		return errors.New("sing-box executable path is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), configCheckTimeout)
	defer cancel()
	output := &boundedBuffer{}
	cmd := exec.CommandContext(ctx, s.exePath, "--disable-color", "check", "-c", "stdin")
	cmd.Dir = executableDir(s.exePath)
	cmd.Stdin = strings.NewReader(configJSON)
	cmd.Stdout = io.MultiWriter(output)
	cmd.Stderr = io.MultiWriter(output)
	configureCommand(cmd)
	if err := cmd.Run(); err != nil {
		message := "sing-box configuration check failed: " + err.Error()
		if detail := strings.TrimSpace(output.String()); detail != "" {
			message += "\n" + criticalOutput(detail)
		}
		return errors.New(message)
	}
	return nil
}

func (s *Supervisor) setState(state State, message string) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
	if s.logger != nil {
		s.logger.Log("Core", strings.TrimSpace(message))
	}
	s.notify(Event{Kind: EventState, State: state, Message: message})
}

func (s *Supervisor) Start(configJSON string) error {
	if err := s.Stop(context.Background()); err != nil {
		return err
	}
	if strings.TrimSpace(s.exePath) == "" {
		return errors.New("sing-box executable path is empty")
	}
	s.setState(StateStarting, "starting sing-box")

	output := &boundedBuffer{}
	cmd := exec.Command(s.exePath, "--disable-color", "run", "-c", "stdin")
	cmd.Dir = executableDir(s.exePath)
	cmd.Stdin = strings.NewReader(configJSON)
	cmd.Stdout = io.MultiWriter(output)
	cmd.Stderr = io.MultiWriter(output)
	configureCommand(cmd)
	if err := cmd.Start(); err != nil {
		s.setState(StateFailed, "sing-box start failed: "+err.Error())
		return err
	}
	job, err := attachProcessJob(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		s.setState(StateFailed, "cannot attach sing-box to a job: "+err.Error())
		return err
	}

	s.mu.Lock()
	s.stopRequested = false
	s.cmd = cmd
	s.process = cmd.Process
	s.job = job
	s.output = output
	s.done = make(chan struct{})
	s.gen++
	gen := s.gen
	done := s.done
	s.mu.Unlock()
	startupExit := make(chan error, 1)
	go s.wait(gen, cmd, done, startupExit)
	timer := time.NewTimer(startupGracePeriod)
	defer timer.Stop()
	select {
	case err := <-startupExit:
		message := "sing-box stopped during startup"
		if err != nil {
			message = "sing-box failed during startup: " + err.Error()
		}
		if output := strings.TrimSpace(s.LastOutput()); output != "" {
			message += "\n" + criticalOutput(output)
		}
		return errors.New(message)
	case <-timer.C:
		s.setState(StateRunning, "sing-box running")
		return nil
	}
}

func (s *Supervisor) wait(gen uint64, cmd *exec.Cmd, done chan struct{}, startupExit chan<- error) {
	err := cmd.Wait()
	s.mu.Lock()
	if gen != s.gen {
		s.mu.Unlock()
		return
	}
	job := s.job
	stopRequested := s.stopRequested
	s.job = nil
	s.cmd = nil
	s.process = nil
	s.mu.Unlock()
	closeProcessJob(job)
	if err != nil && !stopRequested {
		message := "sing-box process exited unexpectedly: " + err.Error()
		if output := s.LastOutput(); strings.TrimSpace(output) != "" {
			message += "\n" + criticalOutput(output)
		}
		s.setState(StateFailed, message)
	} else {
		s.setState(StateStopped, "sing-box stopped")
	}
	close(done)
	if startupExit != nil {
		startupExit <- err
	}
}

func criticalOutput(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "FATAL") || strings.HasPrefix(line, "PANIC") || strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") {
			if len(line) > 2000 {
				line = line[:2000]
			}
			return line
		}
	}
	return strings.TrimSpace(output)
}

func waitForStopCompletion(cleanup func(), done <-chan struct{}, ctx context.Context, wait time.Duration, force func()) {
	if cleanup != nil {
		defer cleanup()
	}
	timer := time.NewTimer(wait)
	stopTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	select {
	case <-done:
		stopTimer()
	case <-ctx.Done():
		stopTimer()
		if force != nil {
			force()
		}
	case <-timer.C:
		if force != nil {
			force()
		}
	}

	// Wait for the goroutine to release the job/process handles. A short
	// bounded wait keeps shutdown responsive even if an OS API misbehaves.
	select {
	case <-done:
	case <-time.After(time.Second):
		// The wait goroutine still owns the process handle. Leave it to finish
		// rather than touching a handle that could be reused by a new process.
	}
}

func (s *Supervisor) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	process := s.process
	done := s.done
	if process == nil {
		s.mu.Unlock()
		// The wait goroutine clears process just before publishing done. Wait
		// for that publication so a concurrent Start cannot race the previous
		// process's final state notification.
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	s.state = StateStopping
	s.stopRequested = true
	s.mu.Unlock()
	s.notify(Event{Kind: EventState, State: StateStopping, Message: "stopping sing-box"})

	cleanup, signalErr := requestGracefulStop(process)
	if signalErr != nil && s.logger != nil {
		s.logger.Log("Core", "graceful stop signal failed: "+signalErr.Error())
	}
	wait := 10 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		wait = time.Until(deadline)
		if wait < 0 {
			wait = 0
		}
	}
	waitForStopCompletion(cleanup, done, ctx, wait, func() { s.forceKill(process) })
	return nil
}

func (s *Supervisor) forceKill(process *os.Process) {
	if process == nil {
		return
	}
	// Closing a Windows Job Object terminates the whole tree, including any
	// descendants that may still hold the stdout/stderr pipe open. Clear the
	// field under the same lock used by wait so ownership is unambiguous.
	s.mu.Lock()
	var job processJob
	if s.process == process {
		job = s.job
		s.job = nil
	}
	s.mu.Unlock()
	closeProcessJob(job)
	_ = process.Kill()
}

func (s *Supervisor) Close() error { return s.Stop(context.Background()) }
