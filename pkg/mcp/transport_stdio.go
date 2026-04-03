package mcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// StdioTransport communicates with an MCP server over stdin/stdout of a child process.
// Suitable for local tools like filesystem, git, etc.
type StdioTransport struct {
	command string
	args    []string
	env     []string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	incoming chan []byte
	done     chan struct{}
}

// NewStdioTransport creates a transport that spawns a local subprocess.
func NewStdioTransport(command string, args []string, env []string) *StdioTransport {
	return &StdioTransport{
		command:  command,
		args:     args,
		env:      env,
		incoming: make(chan []byte, 64),
		done:     make(chan struct{}),
	}
}

func (t *StdioTransport) Start(ctx context.Context) error {
	t.cmd = exec.CommandContext(ctx, t.command, t.args...)
	if len(t.env) > 0 {
		t.cmd.Env = t.env
	}

	var err error
	if t.stdin, err = t.cmd.StdinPipe(); err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	if t.stdout, err = t.cmd.StdoutPipe(); err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if t.stderr, err = t.cmd.StderrPipe(); err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := t.cmd.Start(); err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	go t.readLoop()
	go io.Copy(io.Discard, t.stderr)

	return nil
}

func (t *StdioTransport) Send(_ context.Context, data []byte) error {
	data = append(data, '\n')
	_, err := t.stdin.Write(data)
	return err
}

func (t *StdioTransport) Receive() <-chan []byte {
	return t.incoming
}

func (t *StdioTransport) Close() error {
	select {
	case <-t.done:
		return nil
	default:
		close(t.done)
	}

	if t.stdin != nil {
		t.stdin.Close()
	}

	if t.cmd != nil && t.cmd.Process != nil {
		doneCh := make(chan error, 1)
		go func() { doneCh <- t.cmd.Wait() }()

		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
			t.cmd.Process.Kill()
			<-doneCh
		}
	}
	return nil
}

func (t *StdioTransport) readLoop() {
	defer close(t.incoming)

	scanner := bufio.NewScanner(t.stdout)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-t.done:
			return
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Copy bytes since scanner reuses its buffer
		msg := make([]byte, len(line))
		copy(msg, line)

		select {
		case t.incoming <- msg:
		case <-t.done:
			return
		}
	}
}
