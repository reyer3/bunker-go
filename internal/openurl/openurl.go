// Package openurl opens a web link with the desktop's opener, as an
// external process (bunker links no native code): xdg-open on Linux,
// open on macOS, or the command in BUNKER_OPEN_URL, which gets the URL as
// its last argument.
package openurl

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// Env names the variable that overrides the opener command.
const Env = "BUNKER_OPEN_URL"

// Command returns the argv that opens rawURL. Only http(s) links are
// opened: a meeting link comes from a message anyone can send, so a
// file:, javascript: or custom-scheme URL must never reach the opener.
// The URL is a single argv element, never passed through a shell.
func Command(rawURL string, getenv func(string) string) ([]string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("openurl: la reunión no tiene enlace para unirse")
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("openurl: enlace no permitido %q (solo http o https)", rawURL)
	}
	if custom := strings.Fields(getenv(Env)); len(custom) > 0 {
		return append(custom, rawURL), nil
	}
	if runtime.GOOS == "darwin" {
		return []string{"open", rawURL}, nil
	}
	return []string{"xdg-open", rawURL}, nil
}

// Open starts the opener for rawURL and returns once it has started, not
// when it exits (a browser launched by the override may run for hours).
// A missing opener is an error naming BUNKER_OPEN_URL.
func Open(rawURL string, getenv func(string) string) error {
	argv, err := Command(rawURL, getenv)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("openurl: no se encontró %q para abrir el enlace; instala xdg-open o define %s", argv[0], Env)
	}
	cmd := exec.Command(bin, argv[1:]...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("openurl: no se pudo abrir el enlace con %q: %w", argv[0], err)
	}
	go cmd.Wait() // reap it; its outcome is the opener's business
	return nil
}
