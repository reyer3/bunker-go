package core

import (
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// documentExtensionMIME names the document formats content sniffing gets
// wrong. Office Open XML and OpenDocument files are ZIP containers, so
// http.DetectContentType calls them "application/zip"; a spreadsheet sent
// with that type reached the other side as a .zip nobody could open. The
// legacy Office formats sniff as a generic binary and CSV as plain text.
// Their extensions are unambiguous, so for these the extension wins, and
// the table is built in rather than read from the system's mime.types,
// which a minimal install may lack.
var documentExtensionMIME = map[string]string{
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xlsm": "application/vnd.ms-excel.sheet.macroEnabled.12",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".xls":  "application/vnd.ms-excel",
	".doc":  "application/msword",
	".ppt":  "application/vnd.ms-powerpoint",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odt":  "application/vnd.oasis.opendocument.text",
	".odp":  "application/vnd.oasis.opendocument.presentation",
	".csv":  "text/csv",
	".epub": "application/epub+zip",
	".apk":  "application/vnd.android.package-archive",
}

// AttachmentMIME is the MIME type every channel sends a file with: the
// known document extensions above first, then the sniffed content type
// of head (the file's first bytes), then, when sniffing is inconclusive,
// the system's mapping for the extension.
func AttachmentMIME(path string, head []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	if mimeType, ok := documentExtensionMIME[ext]; ok {
		return mimeType
	}
	mimeType := http.DetectContentType(head)
	if mimeType == "application/octet-stream" {
		if guessed := mime.TypeByExtension(ext); guessed != "" {
			mimeType = guessed
		}
	}
	return mimeType
}
