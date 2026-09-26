package tui

import (
	"strings"
	"unicode"
)

// sanitizeTerminalText removes terminal escape sequences as units, not just
// their introducers, so parameters cannot leak into message text. Only line
// breaks survive from control characters.
func sanitizeTerminalText(value string) string {
	runes := []rune(value)
	var out strings.Builder
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\x1b':
			i = skipEscape(runes, i)
		case '\u009b': // C1 CSI
			i = skipCSI(runes, i+1)
		case '\u009d': // C1 OSC
			i = skipStringControl(runes, i+1, true)
		case '\u0090', '\u0098', '\u009e', '\u009f': // C1 DCS, SOS, PM, APC
			i = skipStringControl(runes, i+1, false)
		case '\n':
			out.WriteByte('\n')
		case '\t':
			out.WriteByte(' ')
		default:
			if !unicode.IsControl(runes[i]) && !unicode.Is(unicode.Cf, runes[i]) && runes[i] != '\u2028' && runes[i] != '\u2029' {
				out.WriteRune(runes[i])
			}
		}
	}
	return out.String()
}

func skipEscape(runes []rune, at int) int {
	if at+1 >= len(runes) {
		return at
	}
	switch runes[at+1] {
	case '[':
		return skipCSI(runes, at+2)
	case ']':
		return skipStringControl(runes, at+2, true)
	case 'P', 'X', '^', '_':
		return skipStringControl(runes, at+2, false)
	default:
		// Fe and legacy ESC sequences can have intermediate bytes before a
		// final byte. Consume the entire sequence, including its final byte.
		for i := at + 1; i < len(runes); i++ {
			if runes[i] >= 0x30 && runes[i] <= 0x7e {
				return i
			}
		}
		return len(runes) - 1
	}
}

func skipCSI(runes []rune, at int) int {
	for i := at; i < len(runes); i++ {
		if runes[i] >= 0x40 && runes[i] <= 0x7e {
			return i
		}
	}
	return len(runes) - 1
}

func skipStringControl(runes []rune, at int, bellTerminates bool) int {
	for i := at; i < len(runes); i++ {
		if runes[i] == '\u009c' || (bellTerminates && runes[i] == '\a') {
			return i
		}
		if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '\\' {
			return i + 1
		}
	}
	return len(runes) - 1
}
