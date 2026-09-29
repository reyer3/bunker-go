package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/reyer3/bunker-go/internal/tui"
)

type entryTestClient struct {
	tui.Client
	closed int
}

func (c *entryTestClient) Close() error {
	c.closed++
	return nil
}

func TestRunWithDependenciesNoArgsStartsTUIOnTerminalAndClosesClient(t *testing.T) {
	client := &entryTestClient{}
	started := false
	deps := runDependencies{
		isTerminal: func(*os.File) bool { return true },
		dial:       func(context.Context, string) (tui.Client, error) { return client, nil },
		startTUI: func(got tui.Client, _ io.Reader, _ io.Writer, _ tuiLaunch) error {
			started = true
			if got == client {
				t.Fatal("TUI bypassed the query reconnect wrapper")
			}
			return nil
		},
	}

	code := runWithDependencies(nil, os.Stdin, io.Discard, io.Discard, deps)
	if code != 0 {
		t.Fatalf("run exit code = %d, want 0", code)
	}
	if !started {
		t.Fatal("TUI was not started")
	}
	if client.closed != 1 {
		t.Fatalf("client close count = %d, want 1", client.closed)
	}
}

func TestRunWithDependenciesNoArgsWithoutTerminalKeepsUsageExit(t *testing.T) {
	dialed := false
	started := false
	deps := runDependencies{
		isTerminal: func(*os.File) bool { return false },
		dial: func(context.Context, string) (tui.Client, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		},
		startTUI: func(tui.Client, io.Reader, io.Writer, tuiLaunch) error {
			started = true
			return nil
		},
	}
	var stderr bytes.Buffer

	code := runWithDependencies(nil, os.Stdin, io.Discard, &stderr, deps)
	if code != 2 {
		t.Fatalf("run exit code = %d, want 2", code)
	}
	if dialed || started {
		t.Fatalf("no-terminal path dialed=%v started=%v, want neither", dialed, started)
	}
	if stderr.Len() == 0 {
		t.Fatal("no-terminal path did not print usage")
	}
}

func TestRunWithDependenciesTUIErrorReturnsFailureAndClosesClient(t *testing.T) {
	client := &entryTestClient{}
	deps := runDependencies{
		isTerminal: func(*os.File) bool { return true },
		dial:       func(context.Context, string) (tui.Client, error) { return client, nil },
		startTUI:   func(tui.Client, io.Reader, io.Writer, tuiLaunch) error { return errors.New("terminal failed") },
	}

	code := runWithDependencies(nil, os.Stdin, io.Discard, io.Discard, deps)
	if code != 1 {
		t.Fatalf("run exit code = %d, want 1", code)
	}
	if client.closed != 1 {
		t.Fatalf("client close count = %d, want 1", client.closed)
	}
}

func TestRunWithDependenciesLeavesSubcommandsOnCLIPath(t *testing.T) {
	started := false
	deps := runDependencies{
		isTerminal: func(*os.File) bool { return true },
		dial: func(context.Context, string) (tui.Client, error) {
			return nil, errors.New("subcommand should not use TUI dial")
		},
		startTUI: func(tui.Client, io.Reader, io.Writer, tuiLaunch) error {
			started = true
			return nil
		},
	}

	code := runWithDependencies([]string{"help"}, os.Stdin, io.Discard, io.Discard, deps)
	if code != 0 {
		t.Fatalf("help exit code = %d, want 0", code)
	}
	if started {
		t.Fatal("help unexpectedly started the TUI")
	}
}
