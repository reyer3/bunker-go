package core

import "testing"

// zipHead is the start of every ZIP container, which is what an .xlsx,
// .docx or .ods is on disk.
var zipHead = []byte("PK\x03\x04\x14\x00\x06\x00\x08\x00\x00\x00!\x00")

// TestAttachmentMIMEOfficeDocuments pins the fix for spreadsheets that
// reached the other side as a .zip nobody could open: an Office or
// OpenDocument file is sent with its own type, not the "application/zip"
// its bytes sniff as.
func TestAttachmentMIMEOfficeDocuments(t *testing.T) {
	cases := map[string]string{
		"ventas.xlsx":  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"VENTAS.XLSX":  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"informe.docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"slides.pptx":  "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"hoja.ods":     "application/vnd.oasis.opendocument.spreadsheet",
	}
	for name, want := range cases {
		if got := AttachmentMIME(name, zipHead); got != want {
			t.Errorf("AttachmentMIME(%q) = %q, want %q", name, got, want)
		}
	}
	if got := AttachmentMIME("viejo.xls", []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}); got != "application/vnd.ms-excel" {
		t.Errorf("legacy .xls = %q, want application/vnd.ms-excel", got)
	}
	if got := AttachmentMIME("datos.csv", []byte("a,b\n1,2\n")); got != "text/csv" {
		t.Errorf(".csv = %q, want text/csv", got)
	}
}

// TestAttachmentMIMEKeepsSniffing pins that everything else is still
// sniffed: a real .zip stays a zip, and a PNG is a PNG whatever its name.
func TestAttachmentMIMEKeepsSniffing(t *testing.T) {
	if got := AttachmentMIME("fotos.zip", zipHead); got != "application/zip" {
		t.Errorf(".zip = %q, want application/zip", got)
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	if got := AttachmentMIME("foto.dat", png); got != "image/png" {
		t.Errorf("png named .dat = %q, want image/png", got)
	}
	if got := AttachmentMIME("informe.pdf", []byte("%PDF-1.7\n")); got != "application/pdf" {
		t.Errorf(".pdf = %q, want application/pdf", got)
	}
}
