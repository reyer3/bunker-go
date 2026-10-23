package mail

import (
	"encoding/base64"
	"strings"
	"testing"
)

// relatedMessage builds a raw RFC 5322 multipart/related message the way
// marketing mail and news digests embed images in the body: the HTML part
// references an attached-by-value image through a cid: URL, the image part
// carries Content-Disposition: inline, and the whole thing travels in a
// multipart/mixed wrapper with one real attachement behind it.
func relatedMessage(png []byte, withFilename bool) string {
	disposition := "inline\r\n"
	if withFilename {
		disposition = "inline; filename=logo.jpg\r\n"
	}
	contentType := "image/png\r\n"
	if withFilename {
		contentType = "image/jpeg\r\n"
	}
	return "From: Alice <alice@example.org>\r\n" +
		"To: alice@example.org\r\n" +
		"Subject: Digest\r\n" +
		"Message-Id: <related@example.org>\r\n" +
		"Date: Fri, 25 Sep 2026 10:00:00 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=OUTER\r\n" +
		"\r\n" +
		"--OUTER\r\n" +
		"Content-Type: multipart/related; boundary=REL\r\n" +
		"\r\n" +
		"--REL\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>Hello <img src=\"cid:img1\"> cadou</p>\r\n" +
		"--REL\r\n" +
		"Content-Type: " + strings.TrimSuffix(contentType, "\r\n") + "\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: " + strings.TrimSuffix(disposition, "\r\n") + "\r\n" +
		"Content-Id: <img1>\r\n" +
		"\r\n" +
		base64.StdEncoding.EncodeToString(png) + "\r\n" +
		"--REL--\r\n" +
		"--OUTER\r\n" +
		"Content-Type: application/pdf; name=doc.pdf\r\n" +
		"Content-Disposition: attachment; filename=doc.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		base64.StdEncoding.EncodeToString([]byte("fake pdf bytes")) + "\r\n" +
		"--OUTER--\r\n"
}

const fakePNG = "fake png bytes"

// RED: an embedded image must be listed as an attachment row, not read and
// discarded, so a client can download it with the regular downloader.
func TestInlineImageBecomesAttachment(t *testing.T) {
	_, attachments, _, _ := walkMessage([]byte(relatedMessage([]byte(fakePNG), false)), false)
	if len(attachments) != 2 {
		t.Fatalf("len(Attachments) = %d, want 2 (inline image + pdf)", len(attachments))
	}
	att := attachments[0]
	if !att.Inline {
		t.Error("first attachment Inline = false, want true")
	}
	if att.MIME != "image/png" {
		t.Errorf("MIME = %q, want image/png", att.MIME)
	}
	if att.Name != "cid-1.png" {
		t.Errorf("Name = %q, want synthetic cid-1.png", att.Name)
	}
	if att.Size != int64(len(fakePNG)) {
		t.Errorf("Size = %d, want %d", att.Size, len(fakePNG))
	}
	if att.Ref != "part-1" {
		t.Errorf("Ref = %q, want part-1", att.Ref)
	}
	doc := attachments[1]
	if doc.Inline {
		t.Error("second attachment Inline = true, want false (real attachment)")
	}
	if doc.Name != "doc.pdf" || doc.Ref != "part-2" {
		t.Errorf("second attachment = %+v, want doc.pdf part-2", doc)
	}
}

// An inline image part that carries its own filename keeps it.
func TestInlineImageKeepsFilename(t *testing.T) {
	_, attachments, _, _ := walkMessage([]byte(relatedMessage([]byte(fakePNG), true)), false)
	if len(attachments) != 2 {
		t.Fatalf("len(Attachments) = %d, want 2", len(attachments))
	}
	if name := attachments[0].Name; name != "logo.jpg" {
		t.Errorf("Name = %q, want logo.jpg", name)
	}
	if mime := attachments[0].MIME; mime != "image/jpeg" {
		t.Errorf("MIME = %q, want image/jpeg", mime)
	}
}

// download must address the same enumeration, so index 0 is the inline
// image (first attachment row) and index 1 the PDF.
func TestAttachmentPartAtIndexesInlineImages(t *testing.T) {
	raw := []byte(relatedMessage([]byte(fakePNG), false))

	data, err := attachmentPartAt(raw, 0)
	if err != nil {
		t.Fatalf("attachmentPartAt(0) error = %v", err)
	}
	if string(data) != fakePNG {
		t.Errorf("attachmentPartAt(0) = %q, want the inline image bytes", data)
	}
	data, err = attachmentPartAt(raw, 1)
	if err != nil {
		t.Fatalf("attachmentPartAt(1) error = %v", err)
	}
	if string(data) != "fake pdf bytes" {
		t.Errorf("attachmentPartAt(1) = %q, want the pdf bytes", data)
	}
	if _, err = attachmentPartAt(raw, 2); err == nil {
		t.Error("attachmentPartAt(2) error = nil, want out of range")
	}
}

// Truncated bodies keep the existing rule: a partially read inline image
// must not produce a row, because its size is already wrong.
func TestTruncatedInlineImageYieldsNoRow(t *testing.T) {
	raw := relatedMessage([]byte(fakePNG), false)
	// Cut a few bytes into the image's base64 payload, the way sync does
	// when it shortens the body.
	cut := strings.Index(raw, base64.StdEncoding.EncodeToString([]byte(fakePNG))) + 10
	truncated := []byte(raw[:cut])
	_, attachments, _, err := walkMessage(truncated, true)
	if err != nil {
		t.Fatalf("walkMessage(truncated) error = %v", err)
	}
	for _, a := range attachments {
		if a.Inline {
			t.Errorf("truncated walk produced inline row %+v; must not", a)
		}
	}
}
