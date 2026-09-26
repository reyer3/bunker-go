package mail

import (
	"html"
	"regexp"
	"strings"
)

// scriptStyleRe strips <script>...</script> and <style>...</style>
// (including their content) before any other tag handling, since their
// text content is not human-readable body text.
var scriptStyleRe = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</\s*(script|style)\s*>`)

// blockBreakRe matches the closing or self-closing tags of elements that
// read as a line break in plain text.
var blockBreakRe = regexp.MustCompile(`(?i)</\s*(p|div|tr|li|h[1-6])\s*>|<br\s*/?>`)

// tagRe matches any remaining tag, to be dropped.
var tagRe = regexp.MustCompile(`<[^>]*>`)

// blankLinesRe collapses runs of blank lines left by adjacent block
// breaks.
var blankLinesRe = regexp.MustCompile(`\n{2,}`)

// spaceRunRe collapses runs of horizontal whitespace (but not the
// newlines HTMLToText itself inserts) into a single space.
var spaceRunRe = regexp.MustCompile(`[ \t\r]+`)

// HTMLToText is a simple, dependency-free fallback for rendering an HTML
// mail body as plain text when no text/plain part is available. It is
// not a full HTML renderer: it strips tags, turns block-level
// boundaries into line breaks, decodes entities and collapses
// whitespace.
func HTMLToText(in string) string {
	s := scriptStyleRe.ReplaceAllString(in, "")
	s = blockBreakRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		line = spaceRunRe.ReplaceAllString(line, " ")
		lines[i] = strings.TrimSpace(line)
	}
	s = strings.Join(lines, "\n")
	s = blankLinesRe.ReplaceAllString(s, "\n")
	return strings.Trim(s, "\n")
}
