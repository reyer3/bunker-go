package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

const attachmentItemID = "mail:work:INBOX:1:9"

// Attachment indexes in attachmentBackend's item.
const (
	attText = iota
	attHTML
	attDOCX
	attXLSX
	attPDF
	attImage
	attBig
	attLatin1
	attLegacyDoc
	attODT
)

// zipOf builds a zip in memory from name → content, the shape of a docx,
// xlsx or odt.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func attachmentBackend(t *testing.T) *fakeBackend {
	t.Helper()
	docx := zipOf(t, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types/>`,
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Contrato de servicios</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">Cláusula </w:t></w:r><w:r><w:t>primera</w:t></w:r><w:r><w:tab/><w:t>plazo</w:t></w:r></w:p>
<w:p><w:r><w:instrText>PAGE</w:instrText></w:r></w:p>
</w:body></w:document>`,
	})
	xlsx := zipOf(t, map[string]string{
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="3" uniqueCount="3">
<si><t>Concepto</t></si><si><t>Monto</t></si><si><r><t>Luz</t></r><r><t> eléctrica</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>120.5</v></c></row>
<row r="3"><c r="A3" t="inlineStr"><is><t>Total</t></is></c><c r="B3"><f>SUM(B2)</f><v>120.5</v></c></row>
</sheetData></worksheet>`,
	})
	odt := zipOf(t, map[string]string{
		"content.xml": `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"><office:body><office:text><text:h>Acta</text:h><text:p>Se acuerda<text:s/>pagar.</text:p></office:text></office:body></office:document-content>`,
	})

	backend := mcpBackend()
	backend.items[attachmentItemID] = core.Item{
		ID: attachmentItemID, Channel: core.ChannelMail, Account: "work", From: core.Address{ID: "someone@example.org"},
		Subject: "documentos", Unread: true, Timestamp: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC),
		Attachments: []core.Attachment{
			attText:      {Name: "notas.txt", MIME: "text/plain", Size: 10},
			attHTML:      {Name: "aviso.html", MIME: "text/html"},
			attDOCX:      {Name: "contrato.docx", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
			attXLSX:      {Name: "cuadro.xlsx", MIME: "application/octet-stream"},
			attPDF:       {Name: "factura.pdf", MIME: "application/pdf"},
			attImage:     {Name: "foto.jpg", MIME: "image/jpeg", Size: 2048},
			attBig:       {Name: "grande.txt", MIME: "text/plain"},
			attLatin1:    {Name: "antiguo.txt", MIME: "text/plain"},
			attLegacyDoc: {Name: "viejo.doc", MIME: "application/msword"},
			attODT:       {Name: "acta.odt", MIME: "application/vnd.oasis.opendocument.text"},
		},
	}
	backend.downloadData = map[int][]byte{
		attText:      []byte("Pagar el 30 de septiembre.\n"),
		attHTML:      []byte("<html><head><style>p{color:red}</style></head><body><p>Hola &amp; adiós</p><p>Saldo: 10</p></body></html>"),
		attDOCX:      docx,
		attXLSX:      xlsx,
		attPDF:       []byte("%PDF-1.4 fake"),
		attImage:     []byte("not really a jpeg"),
		attBig:       []byte(strings.Repeat("ñ", mcpAttachmentTextLimit)), // 2 bytes each: twice the cap
		attLatin1:    {'c', 'a', 'f', 0xE9},                               // "café" in Windows-1252
		attLegacyDoc: []byte("binary"),
		attODT:       odt,
	}
	return backend
}

func attachmentText(t *testing.T, backend *fakeBackend, index int) map[string]any {
	t.Helper()
	s := mcpSession(t, backend, false)
	res, out := callTool(t, s, "attachment", map[string]any{"id": attachmentItemID, "index": index})
	if res.IsError {
		t.Fatalf("attachment %d: %s", index, toolText(res))
	}
	return out
}

func TestMCPAttachmentPlainText(t *testing.T) {
	backend := attachmentBackend(t)
	out := attachmentText(t, backend, attText)
	if out["text"] != "Pagar el 30 de septiembre." || out["has_text"] != true || out["format"] != "text" || out["name"] != "notas.txt" {
		t.Fatalf("plain text: %v", out)
	}
	if out["truncated"] != nil {
		t.Fatalf("a short text is not truncated: %v", out)
	}
	if len(backend.downloadCalls) != 1 || backend.downloadCalls[0].Index != attText || backend.downloadCalls[0].ID != attachmentItemID {
		t.Fatalf("download calls: %+v", backend.downloadCalls)
	}
	// The temp dir the daemon wrote into is gone afterwards.
	if _, err := os.Stat(filepath.Dir(backend.downloadCalls[0].DestPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp dir should be removed, stat: %v", err)
	}
	// Reading an attachment never marks the item read.
	for _, c := range backend.readCalls {
		if c.MarkReceipt {
			t.Fatalf("attachment must never mark the item read: %+v", backend.readCalls)
		}
	}
	if len(backend.organizeCalls) != 0 {
		t.Fatalf("attachment must never organize: %+v", backend.organizeCalls)
	}
}

func TestMCPAttachmentCharset(t *testing.T) {
	out := attachmentText(t, attachmentBackend(t), attLatin1)
	if out["text"] != "café" {
		t.Fatalf("Windows-1252 text should be decoded: %q", out["text"])
	}

	backend := attachmentBackend(t)
	item := backend.items[attachmentItemID]
	item.Attachments[attLatin1].MIME = "text/plain; charset=iso-8859-1"
	out = attachmentText(t, backend, attLatin1)
	if out["text"] != "café" {
		t.Fatalf("a declared charset should be decoded: %q", out["text"])
	}
}

func TestMCPAttachmentHTML(t *testing.T) {
	out := attachmentText(t, attachmentBackend(t), attHTML)
	text, _ := out["text"].(string)
	if out["format"] != "html" || !strings.Contains(text, "Hola & adiós") || !strings.Contains(text, "Saldo: 10") {
		t.Fatalf("html: %v", out)
	}
	if strings.Contains(text, "<") || strings.Contains(text, "color:red") {
		t.Fatalf("html tags and styles must be stripped: %q", text)
	}
}

func TestMCPAttachmentDOCX(t *testing.T) {
	out := attachmentText(t, attachmentBackend(t), attDOCX)
	want := "Contrato de servicios\nCláusula primera\tplazo"
	if out["format"] != "docx" || out["text"] != want {
		t.Fatalf("docx: got %q, want %q (%v)", out["text"], want, out)
	}
}

func TestMCPAttachmentXLSX(t *testing.T) {
	// MIME octet-stream: the .xlsx extension picks the reader.
	out := attachmentText(t, attachmentBackend(t), attXLSX)
	want := "Concepto\tMonto\nLuz eléctrica\t120.5\nTotal\t120.5"
	if out["format"] != "xlsx" || out["text"] != want {
		t.Fatalf("xlsx: got %q, want %q (%v)", out["text"], want, out)
	}
}

func TestMCPAttachmentODT(t *testing.T) {
	out := attachmentText(t, attachmentBackend(t), attODT)
	if out["format"] != "odt" || out["text"] != "Acta\nSe acuerda pagar." {
		t.Fatalf("odt: %v", out)
	}
}

func TestMCPAttachmentPDF(t *testing.T) {
	// A fake pdftotext that only uses shell builtins (PATH holds nothing
	// else) and checks it was handed the downloaded file.
	bin := t.TempDir()
	script := "#!/bin/sh\n[ -f \"$4\" ] || { echo \"no file: $4\" >&2; exit 3; }\nprintf 'Factura 001\\fTotal: 100.00\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "pdftotext"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	out := attachmentText(t, attachmentBackend(t), attPDF)
	if out["format"] != "pdf" || out["text"] != "Factura 001\n\nTotal: 100.00" {
		t.Fatalf("pdf: %v", out)
	}
}

func TestMCPAttachmentPDFWithoutPDFToText(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := mcpSession(t, attachmentBackend(t), false)
	res, _ := callTool(t, s, "attachment", map[string]any{"id": attachmentItemID, "index": attPDF})
	if !res.IsError || !strings.Contains(toolText(res), "poppler-utils") {
		t.Fatalf("a PDF without pdftotext must fail suggesting poppler-utils: %s", toolText(res))
	}
}

func TestMCPAttachmentImageIsMetadataOnly(t *testing.T) {
	backend := attachmentBackend(t)
	out := attachmentText(t, backend, attImage)
	if out["format"] != "media" || out["has_text"] != false || out["text"] != nil {
		t.Fatalf("image: %v", out)
	}
	if out["name"] != "foto.jpg" || out["mime"] != "image/jpeg" || out["size"] != float64(2048) {
		t.Fatalf("image metadata: %v", out)
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "no text") {
		t.Fatalf("image needs a note saying there is no text: %v", out)
	}
	if len(backend.downloadCalls) != 0 {
		t.Fatalf("media should not be downloaded: %+v", backend.downloadCalls)
	}
}

func TestMCPAttachmentTruncates(t *testing.T) {
	out := attachmentText(t, attachmentBackend(t), attBig)
	text, _ := out["text"].(string)
	if out["truncated"] != true || len(text) > mcpAttachmentTextLimit || len(text) < mcpAttachmentTextLimit-1 {
		t.Fatalf("big text: truncated=%v, %d bytes", out["truncated"], len(text))
	}
	if !strings.HasSuffix(text, "ñ") {
		t.Fatalf("truncation must cut on a rune boundary: %q", text[len(text)-4:])
	}
}

func TestMCPAttachmentErrors(t *testing.T) {
	s := mcpSession(t, attachmentBackend(t), false)
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"index past the end", map[string]any{"id": attachmentItemID, "index": 99}, "out of range"},
		{"negative index", map[string]any{"id": attachmentItemID, "index": -1}, "out of range"},
		{"item without attachments", map[string]any{"id": "whatsapp:personal:1", "index": 0}, "0 attachments"},
		{"unsupported type", map[string]any{"id": attachmentItemID, "index": attLegacyDoc}, "no text reader"},
		{"empty id", map[string]any{"id": "", "index": 0}, "id is required"},
	} {
		res, _ := callTool(t, s, "attachment", tc.args)
		if !res.IsError || !strings.Contains(toolText(res), tc.want) {
			t.Errorf("%s: want an error containing %q, got %s", tc.name, tc.want, toolText(res))
		}
	}
}

func TestMCPAttachmentReadOnly(t *testing.T) {
	s := mcpSession(t, attachmentBackend(t), false)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "attachment" {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatalf("attachment must be annotated read-only: %+v", tool.Annotations)
			}
			return
		}
	}
	t.Fatal("tool attachment missing")
}
