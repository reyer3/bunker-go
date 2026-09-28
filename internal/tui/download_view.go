package tui

import (
	"fmt"
	"strings"
)

// writeDownload renders the K5/K6 attachment download overlay: the
// picker (2+ attachments), the editable destination path, the
// "¿Sobrescribir?" confirm, an in-flight "Descargando…", the saved
// result, or a clear error.
func (m Model) writeDownload(out *strings.Builder) {
	out.WriteString("Descargar adjunto\n\n")

	if m.downloadResult.Path != "" && m.downloadErr == nil {
		fmt.Fprintf(out, "Guardado: %s (%d bytes)\n\nEsc volver\n", safeLine(m.downloadResult.Path), m.downloadResult.Bytes)
		return
	}

	if m.downloadPicking {
		out.WriteString("Varios adjuntos, elegí uno:\n")
		for i, attachment := range m.downloadAttachments {
			fmt.Fprintf(out, "%d. %s (%d bytes)\n", i+1, safeLine(attachment.Name), attachment.Size)
		}
		out.WriteString("\nEsc cancelar\n")
		return
	}

	if m.downloadIndex >= 0 && m.downloadIndex < len(m.downloadAttachments) {
		attachment := m.downloadAttachments[m.downloadIndex]
		fmt.Fprintf(out, "Adjunto: %s (%d bytes)\n\n", safeLine(attachment.Name), attachment.Size)
	}

	if m.downloadOverwrite {
		fmt.Fprintf(out, "Destino: %s\n\n¿Sobrescribir? ↵ sí · Esc no\n", safeLine(m.downloadPath))
		return
	}
	if m.downloadSending {
		fmt.Fprintf(out, "Destino: %s\n\nDescargando…\n", safeLine(m.downloadPath))
		return
	}
	fmt.Fprintf(out, "Destino: %s\n", safeLine(m.downloadPath))
	if m.downloadErr != nil {
		fmt.Fprintf(out, "\nError: %s\n", humanError(m.downloadErr))
	}
	out.WriteString("\n↵ confirmar · Esc cancelar\n")
}
