package tui

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

func (m Model) View() string {
	if m.helpOpen {
		return m.helpView()
	}
	if m.picker != nil {
		return m.pickerView()
	}
	var out strings.Builder
	if m.composing {
		m.writeCompose(&out)
		return wrapView(out.String(), m.width)
	}
	if m.previewing {
		m.writePreview(&out)
		return wrapView(out.String(), m.width)
	}
	if m.marking {
		m.writeMark(&out)
		return wrapView(out.String(), m.width)
	}
	if m.mailComposing {
		m.writeMailEditor(&out)
		return wrapView(out.String(), m.width)
	}
	if m.downloadActive {
		m.writeDownload(&out)
		return wrapView(out.String(), m.width)
	}
	if m.viewer != nil {
		return m.viewerView()
	}
	if m.detail && m.chatMode {
		return wrapView(strings.Join(m.chatViewLines(), "\n"), m.width)
	}
	if m.detail && m.threadMode {
		return wrapView(strings.Join(m.threadViewLines(), "\n"), m.width)
	}
	if m.detail {
		return wrapView(strings.Join(m.detailViewLines(), "\n"), m.width)
	}
	return m.inboxView()
}

func (m Model) writeCompose(out *strings.Builder) {
	out.WriteString("Responder\n\n")
	out.WriteString(m.composer.View())
	out.WriteString("\n")
	if len(m.attachments) > 0 {
		out.WriteString("\nAdjuntos: ")
		out.WriteString(attachmentChips(m.attachments))
		out.WriteString("\n")
	}
	if m.attaching {
		fmt.Fprintf(out, "\nRuta del adjunto: %s█\n", safeLine(m.attachInput))
	}
	if m.replyErr != nil {
		fmt.Fprintf(out, "\nError: %s\n", humanError(m.replyErr))
	}
	out.WriteString("\n" + hintLine(m.width, composeHints...) + "\n")
}

func (m Model) writePreview(out *strings.Builder) {
	out.WriteString("Vista previa de la respuesta\n\n")
	fmt.Fprintf(out, "Cuenta: %s/%s\n", safeLine(string(m.previewPlan.Channel)), safeLine(m.previewPlan.Account))
	fmt.Fprintf(out, "Para: %s\n", safeLine(strings.Join(m.previewPlan.Recipients, ", ")))
	if len(m.previewPlan.Cc) > 0 {
		fmt.Fprintf(out, "Cc: %s\n", safeLine(strings.Join(m.previewPlan.Cc, ", ")))
	}
	if len(m.previewPlan.Attachments) > 0 {
		out.WriteString("Adjuntos:\n")
		for _, attachment := range m.previewPlan.Attachments {
			fmt.Fprintf(out, "- %s (%s, %d bytes)\n", safeLine(attachment.Name), safeLine(attachment.MIME), attachment.Size)
		}
	}
	out.WriteString("\n")
	out.WriteString(sanitizeTerminalText(m.composer.Value()))
	out.WriteString("\n")
	if m.sending {
		out.WriteString("\nEnviando…\n")
	} else if m.replyErr != nil {
		fmt.Fprintf(out, "\nNo se pudo enviar: %s\n", humanError(m.replyErr))
	}
	if m.quitConfirm {
		out.WriteString("\nPulsa q otra vez para descartar el borrador y salir\n")
	}
	out.WriteString("\n" + hintLine(m.width, confirmSendHints...) + "\n")
}

func (m Model) writeMark(out *strings.Builder) {
	out.WriteString("Marcar como leído\n\n")
	if m.markLoading {
		out.WriteString("Cargando…\n\nEsc cancelar\n")
		return
	}
	if m.markSending {
		out.WriteString("Marcando como leído…\n")
		return
	}
	if m.markErr != nil {
		fmt.Fprintf(out, "Error: %s\n\nEsc cerrar\n", humanError(m.markErr))
		return
	}
	if m.markConfirm {
		fmt.Fprintf(out, "¿Marcar como leído en %s/%s? ↵ confirmar · Esc cancelar\n",
			safeLine(string(m.markPlan.Channel)), safeLine(m.markPlan.Account))
		return
	}
	out.WriteString("Esc cancelar\n")
}

func safeLine(value string) string {
	return strings.ReplaceAll(sanitizeTerminalText(value), "\n", " ")
}

func wrapView(value string, width int) string {
	if width <= 0 {
		return value
	}
	runes := []rune(value)
	var out strings.Builder
	column := 0
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\n' {
			out.WriteRune(r)
			column = 0
			continue
		}
		// An escape sequence (bunker's own OSC 8 hyperlinks — see
		// hyperlink.go) is copied through as one atomic, zero-width
		// unit: it must never count toward the width budget, and never
		// be split across a wrap point, or the escape itself corrupts.
		if r == '\x1b' {
			end := skipEscape(runes, i)
			out.WriteString(string(runes[i : end+1]))
			i = end
			continue
		}
		cellWidth := runewidth.RuneWidth(r)
		if column > 0 && column+cellWidth > width {
			out.WriteByte('\n')
			column = 0
		}
		if cellWidth > width {
			continue
		}
		out.WriteRune(r)
		column += cellWidth
	}
	return out.String()
}
