package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// TestTermPrompterCancels: a prompt blocked on stdin must return as soon
// as the context ends, which is what makes Ctrl-C work at the login
// prompts instead of restarting the read.
func TestTermPrompterCancels(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	p := termPrompter{ctx: ctx, in: bufio.NewReader(pr), fd: -1}

	done := make(chan error, 1)
	go func() {
		_, err := p.Line("identity: ")
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Line did not return after the context was canceled")
	}
}

func TestTermPrompterReadsLine(t *testing.T) {
	pr, pw := io.Pipe()
	go func() { io.WriteString(pw, "  bob \n"); pw.Close() }()
	p := termPrompter{ctx: context.Background(), in: bufio.NewReader(pr), fd: -1}
	got, err := p.Line("identity: ")
	if err != nil || got != "bob" {
		t.Errorf("Line = %q, %v; want \"bob\"", got, err)
	}
	// Secret on a non-TTY falls back to a plain line, so piped input works.
	pr2, pw2 := io.Pipe()
	go func() { io.WriteString(pw2, "hunter2\n"); pw2.Close() }()
	p.in = bufio.NewReader(pr2)
	got, err = p.Secret("password: ")
	if err != nil || got != "hunter2" {
		t.Errorf("Secret = %q, %v", got, err)
	}
}
