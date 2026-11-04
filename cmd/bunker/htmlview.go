package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/net/html"

	"github.com/reyer3/bunker-go/internal/openurl"
)

// openHTMLFile hands a local HTML file to the desktop's opener. A variable
// so tests never launch a browser.
var openHTMLFile = func(path string) error { return openurl.OpenFile(path, os.Getenv) }

// htmlAllowedTags are the elements a mail body keeps. Everything else is
// dropped (its text is kept), and script-like elements are dropped with
// their content: a mail is hostile input rendered in the user's browser.
var htmlAllowedTags = map[string]bool{
	"a": true, "b": true, "blockquote": true, "br": true, "code": true,
	"div": true, "em": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "hr": true, "i": true, "li": true, "ol": true,
	"p": true, "pre": true, "span": true, "strong": true, "table": true,
	"tbody": true, "td": true, "tfoot": true, "th": true, "thead": true,
	"tr": true, "u": true, "ul": true,
}

// htmlDroppedWithContent are elements whose content is never body text.
var htmlDroppedWithContent = map[string]bool{
	"script": true, "style": true, "head": true, "iframe": true,
	"object": true, "embed": true, "noscript": true, "template": true,
	"svg": true, "math": true, "title": true, "form": true,
}

// sanitizeHTML reduces a mail's HTML to inert markup: an allowlist of
// formatting tags, no styles, no event handlers, no images (so nothing is
// fetched from a remote server) and only http(s) and mailto links.
func sanitizeHTML(in string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(in))
	skip := 0 // depth inside a dropped-with-content element
	var skipTag string
	for {
		switch z.Next() {
		case html.ErrorToken:
			return b.String()
		case html.TextToken:
			if skip == 0 {
				b.WriteString(html.EscapeString(string(z.Text())))
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if skip > 0 {
				if tag == skipTag {
					skip++
				}
				continue
			}
			if htmlDroppedWithContent[tag] {
				skip, skipTag = 1, tag
				continue
			}
			if !htmlAllowedTags[tag] {
				continue
			}
			b.WriteString("<" + tag)
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				if tag == "a" && string(k) == "href" && safeLink(string(v)) {
					fmt.Fprintf(&b, ` href="%s" rel="noopener noreferrer"`, html.EscapeString(string(v)))
				}
			}
			b.WriteString(">")
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if skip > 0 {
				if tag == skipTag {
					skip--
				}
				continue
			}
			if htmlAllowedTags[tag] && tag != "br" && tag != "hr" {
				b.WriteString("</" + tag + ">")
			}
		}
	}
}

// safeLink reports whether href may be kept: web and mail links only, so a
// javascript: or data: URL from a message never reaches the browser.
func safeLink(href string) bool {
	h := strings.ToLower(strings.TrimSpace(href))
	return strings.HasPrefix(h, "https://") || strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "mailto:")
}

// htmlPage wraps sanitized markup in a document whose CSP forbids any
// script, style or network request, a second barrier behind the sanitizer.
func htmlPage(subject, body string) string {
	return `<!doctype html><html><head><meta charset="utf-8">` +
		`<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">` +
		`<title>` + html.EscapeString(subject) + `</title>` +
		`<style>body{font-family:sans-serif;max-width:48em;margin:2em auto;padding:0 1em;line-height:1.5}</style>` +
		`</head><body>` + body + `</body></html>`
}

// showHTML writes the sanitized body of a mail to a private temp file and
// opens it, returning the file's path. The file stays: the browser reads
// it after bunker exits.
func showHTML(subject, rawHTML string) (string, error) {
	f, err := os.CreateTemp("", "bunker-mail-*.html")
	if err != nil {
		return "", fmt.Errorf("cmd: read --html: create temp file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(htmlPage(subject, sanitizeHTML(rawHTML))); err != nil {
		return "", fmt.Errorf("cmd: read --html: write %s: %w", f.Name(), err)
	}
	if err := openHTMLFile(f.Name()); err != nil {
		return f.Name(), fmt.Errorf("cmd: read --html: %w", err)
	}
	return f.Name(), nil
}
