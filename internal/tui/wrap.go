package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// wrapWords word-wraps one line of plain text (no "\n", no escapes) to
// width terminal cells, breaking at spaces; a word wider than the line
// (a URL, a long number) starts a line of its own and is hard-split. It
// measures with go-runewidth, the same measure padTo and alignBubbleLine
// use, so every returned line pads to exactly width. A chat bubble used
// to truncate each body line with "…", which cut long messages short in
// any pane narrower than the message.
func wrapWords(text string, width int) []string {
	if width <= 0 || runewidth.StringWidth(text) <= width {
		return []string{text}
	}
	var lines []string
	line, lineWidth := "", 0
	flush := func() {
		lines = append(lines, line)
		line, lineWidth = "", 0
	}
	for i, word := range strings.Split(text, " ") {
		wordWidth := runewidth.StringWidth(word)
		switch {
		case i == 0, lineWidth == 0:
			// No space at the start of a line.
		case lineWidth+1+wordWidth > width:
			flush()
		default:
			line += " "
			lineWidth++
		}
		for wordWidth > width {
			head := runewidth.Truncate(word, width, "")
			if head == "" {
				// A single rune wider than the whole line.
				head = string([]rune(word)[:1])
			}
			if lineWidth > 0 {
				flush()
			}
			line, lineWidth = head, runewidth.StringWidth(head)
			flush()
			word = word[len(head):]
			wordWidth = runewidth.StringWidth(word)
		}
		line += word
		lineWidth += wordWidth
	}
	if line != "" || len(lines) == 0 {
		flush()
	}
	return lines
}
