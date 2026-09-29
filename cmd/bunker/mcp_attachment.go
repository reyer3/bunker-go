package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"

	"github.com/reyer3/bunker-go/internal/channel/mail"
	"github.com/reyer3/bunker-go/internal/core"
)

// The attachment tool (issue #65) lets an agent read the text of an
// invoice, a contract or a spreadsheet someone sent. It downloads the
// attachment through the same daemon path as `bunker download` into a
// private temp dir that is removed afterwards, and extracts the text in
// this process: plain text as is, HTML through the mail package's
// HTMLToText, Office Open XML and ODF by reading their XML out of the
// zip, and PDF through poppler's pdftotext as an external process (the
// module graph has no pure-Go PDF reader, and a PDF parser is far from
// small). Images, audio and video carry no text: the tool answers their
// metadata without downloading them.

// mcpAttachmentTextLimit caps the text the tool returns, in bytes: enough
// for a long contract, small enough for an agent's context.
const mcpAttachmentTextLimit = 100 << 10

// mcpPDFTimeout bounds one pdftotext run, so a pathological PDF cannot
// hang the tool.
const mcpPDFTimeout = 30 * time.Second

// mcpZipEntryLimit caps how much of one zip entry (a docx's
// document.xml, an xlsx sheet) is decompressed, against zip bombs.
const mcpZipEntryLimit = 50 << 20

// errNoPDFToText is returned for a PDF when pdftotext is not installed.
var errNoPDFToText = errors.New("attachment: reading PDF text needs pdftotext, which is not on PATH: install poppler-utils (e.g. apt install poppler-utils, dnf install poppler-utils, brew install poppler)")

type (
	mcpAttachmentIn struct {
		ID    string `json:"id" jsonschema:"the item id, as list or read return it"`
		Index int    `json:"index" jsonschema:"the attachment's position in the item's attachments, starting at 0"`
	}
	mcpAttachmentOut struct {
		ID        string `json:"id"`
		Index     int    `json:"index"`
		Name      string `json:"name"`
		MIME      string `json:"mime,omitempty"`
		Size      int64  `json:"size" jsonschema:"size in bytes"`
		Format    string `json:"format" jsonschema:"how the text was read: text, html, pdf, docx, xlsx, odt, or media (no text)"`
		HasText   bool   `json:"has_text" jsonschema:"whether text was extracted; false comes with a note"`
		Text      string `json:"text,omitempty"`
		Truncated bool   `json:"truncated,omitempty" jsonschema:"the text was cut at the size cap"`
		Note      string `json:"note,omitempty" jsonschema:"why there is no text, when has_text is false"`
	}
)

// attachmentFormat is how an attachment's text is extracted.
type attachmentFormat string

const (
	formatText  attachmentFormat = "text"
	formatHTML  attachmentFormat = "html"
	formatPDF   attachmentFormat = "pdf"
	formatDOCX  attachmentFormat = "docx"
	formatXLSX  attachmentFormat = "xlsx"
	formatODT   attachmentFormat = "odt"
	formatMedia attachmentFormat = "media"
)

func addMCPAttachmentTool(server *mcp.Server, dial mcpDialer) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "attachment",
		Description: "The text of one attachment of an item (id, and index from 0 in the item's attachments), to read invoices, contracts and other documents: " +
			"plain text, CSV, Markdown, JSON, XML, HTML, PDF (needs pdftotext), docx, xlsx and odt. " +
			"Images, audio and video return only name, MIME type and size. Text is capped at 100 KB (truncated: true). Never marks anything read.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAttachmentIn) (*mcp.CallToolResult, mcpAttachmentOut, error) {
		out, err := mcpAttachment(ctx, dial, in)
		return nil, out, err
	})
}

// mcpAttachment downloads the attachment (unless it is media) and
// extracts its text. The daemon connection is released before the
// extraction, which may run pdftotext for a while.
func mcpAttachment(ctx context.Context, dial mcpDialer, in mcpAttachmentIn) (mcpAttachmentOut, error) {
	if in.ID == "" {
		return mcpAttachmentOut{}, errors.New("attachment: id is required")
	}
	// The daemon writes the file (see core.Service.Download) and runs as
	// the same user on the same machine, so a private temp dir works.
	dir, err := os.MkdirTemp("", "bunker-attachment-*")
	if err != nil {
		return mcpAttachmentOut{}, fmt.Errorf("attachment: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	// A fixed name: the attachment's own name is the sender's choice.
	path := filepath.Join(dir, "attachment")

	type fetched struct {
		out    mcpAttachmentOut
		format attachmentFormat
	}
	res, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) (fetched, error) {
		// Read with markReceipt false never marks the item read.
		item, err := b.Read(ctx, in.ID, false)
		if err != nil {
			return fetched{}, err
		}
		if in.Index < 0 || in.Index >= len(item.Attachments) {
			return fetched{}, fmt.Errorf("attachment: %s has %d attachments, index %d is out of range: %w", in.ID, len(item.Attachments), in.Index, core.ErrNotFound)
		}
		att := item.Attachments[in.Index]
		out := mcpAttachmentOut{ID: in.ID, Index: in.Index, Name: att.Name, MIME: att.MIME, Size: att.Size}
		format, ok := attachmentFormatOf(att.Name, att.MIME)
		if !ok {
			return fetched{}, fmt.Errorf("attachment: %q (%s) has no text reader; supported are text, CSV, Markdown, JSON, XML, HTML, PDF, docx, xlsx and odt: %w", att.Name, orUnknown(att.MIME), core.ErrUnsupported)
		}
		if format == formatMedia {
			return fetched{out: out, format: format}, nil
		}
		dl, err := b.Download(ctx, in.ID, in.Index, path, core.DownloadOptions{})
		if err != nil {
			return fetched{}, err
		}
		if dl.Bytes > 0 {
			out.Size = dl.Bytes
		}
		return fetched{out: out, format: format}, nil
	})
	if err != nil {
		return mcpAttachmentOut{}, err
	}
	out := res.out
	out.Format = string(res.format)
	if res.format == formatMedia {
		out.Note = "no text: images, audio and video carry no text to extract; only the metadata is returned"
		return out, nil
	}

	text, err := extractAttachmentText(ctx, res.format, path, out.MIME)
	if err != nil {
		return mcpAttachmentOut{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		out.Note = "no text found in this " + string(res.format)
		if res.format == formatPDF {
			out.Note += " (a scanned PDF is only images and would need OCR)"
		}
		return out, nil
	}
	out.HasText = true
	out.Text, out.Truncated = capText(text, mcpAttachmentTextLimit)
	return out, nil
}

func orUnknown(mimeType string) string {
	if mimeType == "" {
		return "unknown type"
	}
	return mimeType
}

// attachmentFormatOf picks the extractor from the MIME type, falling back
// to the file extension when the MIME type is missing or generic (senders
// often label everything application/octet-stream).
func attachmentFormatOf(name, mimeType string) (attachmentFormat, bool) {
	mt, _, _ := mime.ParseMediaType(mimeType)
	mt = strings.ToLower(mt)
	switch {
	case mt == "application/pdf":
		return formatPDF, true
	case mt == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return formatDOCX, true
	case mt == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return formatXLSX, true
	case mt == "application/vnd.oasis.opendocument.text":
		return formatODT, true
	case mt == "text/html" || mt == "application/xhtml+xml":
		return formatHTML, true
	case strings.HasPrefix(mt, "text/"), mt == "application/json", mt == "application/xml",
		strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"):
		return formatText, true
	case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "video/"):
		return formatMedia, true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return formatPDF, true
	case ".docx":
		return formatDOCX, true
	case ".xlsx":
		return formatXLSX, true
	case ".odt":
		return formatODT, true
	case ".html", ".htm":
		return formatHTML, true
	case ".txt", ".text", ".csv", ".tsv", ".md", ".markdown", ".json", ".xml", ".log":
		return formatText, true
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".bmp", ".tif", ".tiff",
		".mp3", ".m4a", ".ogg", ".opus", ".wav", ".aac", ".amr",
		".mp4", ".mov", ".webm", ".mkv", ".3gp", ".avi":
		return formatMedia, true
	}
	return "", false
}

func extractAttachmentText(ctx context.Context, format attachmentFormat, path, mimeType string) (string, error) {
	switch format {
	case formatText, formatHTML:
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("attachment: %w", err)
		}
		text, err := decodeText(data, mimeType)
		if err != nil {
			return "", err
		}
		if format == formatHTML {
			text = mail.HTMLToText(text)
		}
		return text, nil
	case formatPDF:
		return pdfText(ctx, path)
	case formatDOCX:
		return zipXMLText(path, "word/document.xml", docxXML)
	case formatODT:
		return zipXMLText(path, "content.xml", odtXML)
	case formatXLSX:
		return xlsxText(path)
	}
	return "", fmt.Errorf("attachment: no extractor for %s: %w", format, core.ErrUnsupported)
}

// decodeText turns raw text bytes into UTF-8: the charset the MIME type
// declares; else a byte order mark; else UTF-8 when the bytes are valid;
// else Windows-1252, the usual charset of a legacy Spanish text file.
func decodeText(data []byte, mimeType string) (string, error) {
	var enc encoding.Encoding
	if _, params, err := mime.ParseMediaType(mimeType); err == nil && params["charset"] != "" {
		enc, err = htmlindex.Get(params["charset"])
		if err != nil {
			return "", fmt.Errorf("attachment: charset %q: %w", params["charset"], err)
		}
	}
	switch {
	case enc != nil:
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return string(data[3:]), nil
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		enc = unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM)
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		enc = unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM)
	case utf8.Valid(data):
		return string(data), nil
	default:
		enc = charmap.Windows1252
	}
	out, err := enc.NewDecoder().Bytes(data)
	if err != nil {
		return "", fmt.Errorf("attachment: decode text: %w", err)
	}
	return string(out), nil
}

// capText cuts s to at most limit bytes, on a rune boundary.
func capText(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	i := limit
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i], true
}

// cappedBuffer keeps the first max bytes written to it and discards the
// rest, so a huge pdftotext output cannot exhaust memory.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// pdfText runs poppler's pdftotext as an external process (native
// things stay out of the binary), with a timeout.
func pdfText(ctx context.Context, path string) (string, error) {
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", errNoPDFToText
	}
	ctx, cancel := context.WithTimeout(ctx, mcpPDFTimeout)
	defer cancel()
	// One byte over the cap is enough to know the text was truncated.
	stdout := &cappedBuffer{max: mcpAttachmentTextLimit + 1}
	stderr := &cappedBuffer{max: 4 << 10}
	cmd := exec.CommandContext(ctx, bin, "-enc", "UTF-8", "-q", path, "-")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("attachment: pdftotext did not finish within %s: %w", mcpPDFTimeout, ctx.Err())
		}
		if msg := strings.TrimSpace(stderr.buf.String()); msg != "" {
			return "", fmt.Errorf("attachment: pdftotext: %w: %s", err, msg)
		}
		return "", fmt.Errorf("attachment: pdftotext: %w", err)
	}
	// The cap may cut a rune in half; pdftotext separates pages with
	// form feeds.
	return strings.ReplaceAll(strings.ToValidUTF8(stdout.buf.String(), ""), "\f", "\n\n"), nil
}

// xmlTextRules says which XML elements of a document format carry text
// and which ones read as whitespace. Elements are matched by local name.
type xmlTextRules struct {
	// textIn, when set, keeps only character data inside these elements.
	textIn map[string]bool
	// onStart and onEnd write a separator at an element's start or end.
	onStart map[string]string
	onEnd   map[string]string
}

var (
	// docxXML: text runs are <w:t>, paragraphs <w:p>.
	docxXML = xmlTextRules{
		textIn:  map[string]bool{"t": true},
		onStart: map[string]string{"tab": "\t", "br": "\n", "cr": "\n"},
		onEnd:   map[string]string{"p": "\n"},
	}
	// odtXML: all character data is text; paragraphs and headings are
	// <text:p> and <text:h>.
	odtXML = xmlTextRules{
		onStart: map[string]string{"tab": "\t", "line-break": "\n", "s": " "},
		onEnd:   map[string]string{"p": "\n", "h": "\n"},
	}
)

func xmlText(r io.Reader, rules xmlTextRules) (string, error) {
	dec := xml.NewDecoder(r)
	var b strings.Builder
	inside := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return b.String(), nil
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if rules.textIn[t.Name.Local] {
				inside++
			}
			b.WriteString(rules.onStart[t.Name.Local])
		case xml.EndElement:
			if rules.textIn[t.Name.Local] {
				inside--
			}
			b.WriteString(rules.onEnd[t.Name.Local])
		case xml.CharData:
			if rules.textIn == nil || inside > 0 {
				b.Write(t)
			}
		}
	}
}

// openZipEntry opens one member of a zip, capped at mcpZipEntryLimit.
func openZipEntry(zr *zip.Reader, name string) (io.ReadCloser, error) {
	f, err := zr.Open(name)
	if err != nil {
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(f, mcpZipEntryLimit), f}, nil
}

func zipXMLText(path, entry string, rules xmlTextRules) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("attachment: not a valid document (zip): %w", err)
	}
	defer zr.Close()
	f, err := openZipEntry(&zr.Reader, entry)
	if err != nil {
		return "", fmt.Errorf("attachment: %s: %w", entry, err)
	}
	defer f.Close()
	text, err := xmlText(f, rules)
	if err != nil {
		return "", fmt.Errorf("attachment: %s: %w", entry, err)
	}
	return text, nil
}

type (
	xlsxRichText struct {
		T string `xml:"t"`
		R []struct {
			T string `xml:"t"`
		} `xml:"r"`
	}
	xlsxSharedStrings struct {
		SI []xlsxRichText `xml:"si"`
	}
	xlsxWorksheet struct {
		Rows []struct {
			Cells []struct {
				Type   string       `xml:"t,attr"`
				Value  string       `xml:"v"`
				Inline xlsxRichText `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
)

func (r xlsxRichText) text() string {
	s := r.T
	for _, run := range r.R {
		s += run.T
	}
	return s
}

// xlsxText writes every sheet's rows, one line per row with its cells
// separated by tabs, resolving shared strings. Formulas are left out:
// the cached value is what the sender saw.
func xlsxText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("attachment: not a valid xlsx (zip): %w", err)
	}
	defer zr.Close()

	var shared []string
	if f, err := openZipEntry(&zr.Reader, "xl/sharedStrings.xml"); err == nil {
		var sst xlsxSharedStrings
		err = xml.NewDecoder(f).Decode(&sst)
		f.Close()
		if err != nil {
			return "", fmt.Errorf("attachment: xl/sharedStrings.xml: %w", err)
		}
		for _, si := range sst.SI {
			shared = append(shared, si.text())
		}
	}

	var sheets []string
	for _, f := range zr.File {
		if rest, ok := strings.CutPrefix(f.Name, "xl/worksheets/"); ok && strings.HasSuffix(rest, ".xml") && !strings.Contains(rest, "/") {
			sheets = append(sheets, f.Name)
		}
	}
	if len(sheets) == 0 {
		return "", errors.New("attachment: xlsx has no worksheets")
	}
	// sheet2 before sheet10.
	sort.Slice(sheets, func(i, j int) bool {
		if len(sheets[i]) != len(sheets[j]) {
			return len(sheets[i]) < len(sheets[j])
		}
		return sheets[i] < sheets[j]
	})

	var b strings.Builder
	for _, name := range sheets {
		f, err := openZipEntry(&zr.Reader, name)
		if err != nil {
			return "", fmt.Errorf("attachment: %s: %w", name, err)
		}
		var ws xlsxWorksheet
		err = xml.NewDecoder(f).Decode(&ws)
		f.Close()
		if err != nil {
			return "", fmt.Errorf("attachment: %s: %w", name, err)
		}
		if len(sheets) > 1 {
			fmt.Fprintf(&b, "[%s]\n", strings.TrimSuffix(filepath.Base(name), ".xml"))
		}
		for _, row := range ws.Rows {
			cells := make([]string, 0, len(row.Cells))
			for _, c := range row.Cells {
				switch c.Type {
				case "s":
					i, err := strconv.Atoi(strings.TrimSpace(c.Value))
					if err != nil || i < 0 || i >= len(shared) {
						return "", fmt.Errorf("attachment: %s: bad shared string index %q", name, c.Value)
					}
					cells = append(cells, shared[i])
				case "inlineStr":
					cells = append(cells, c.Inline.text())
				case "b":
					cells = append(cells, map[string]string{"1": "TRUE", "0": "FALSE"}[c.Value])
				default:
					cells = append(cells, c.Value)
				}
			}
			b.WriteString(strings.Join(cells, "\t"))
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}
