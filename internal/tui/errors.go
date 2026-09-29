package tui

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"

	"github.com/reyer3/bunker-go/internal/core"
)

// humanError renders an error for a person reading the panel (issue
// #34): the cases people actually hit get a Spanish sentence that says
// what to do, and anything else keeps its text without the Go package
// prefixes ("rpc: ", "core: ", "whatsapp: "…) that only mean something
// to the code. It never hides the error.
func humanError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case isDaemonDown(err):
		return "sin conexión con el daemon · inícialo con: systemctl --user start bunker"
	case errors.Is(err, context.DeadlineExceeded):
		return "tardó demasiado en responder · vuelve a intentarlo"
	case errors.Is(err, core.ErrNotFound):
		return stripErrorPrefixes(err.Error()) + " (ya no existe)"
	}
	return stripErrorPrefixes(err.Error())
}

// isDaemonDown reports whether err means the daemon is not reachable:
// the socket is missing, refuses connections, or the connection broke.
func isDaemonDown(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed) {
		return true
	}
	// A missing socket reads "dial unix …: connect: no such file or
	// directory"; a missing attachment is also ENOENT, so only the dial
	// form counts.
	msg := err.Error()
	for _, s := range []string{"connection refused", "broken pipe", "connection reset", "rpc: connection closed", "rpc: send request", "cannot reach bunker daemon", "dial unix"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// errorPrefixes are the package prefixes this codebase wraps errors
// with ("pkg: …", see CLAUDE.md).
var errorPrefixes = []string{"rpc", "core", "tui", "store", "whatsapp", "matrix", "mail", "config", "fake", "herdr"}

// stripErrorPrefixes drops every known "pkg: " prefix, wherever the
// wrapping put it: "core: send: whatsapp: x" becomes "send: x".
func stripErrorPrefixes(msg string) string {
	for _, p := range errorPrefixes {
		msg = strings.ReplaceAll(msg, p+": ", "")
	}
	return safeLine(strings.TrimSpace(msg))
}
