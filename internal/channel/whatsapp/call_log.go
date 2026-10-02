package whatsapp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/rs/zerolog"
)

// meowLogger routes meowcaller's zerolog output to log at info level and
// above (warn and error always). meowcaller logs nothing without a logger,
// and its messages are the only evidence of whether media ever flowed
// ("first RTP decoded from relay", "failed to write ... audio").
func meowLogger(log *slog.Logger) zerolog.Logger {
	return zerolog.New(slogWriter{log: log}).Level(zerolog.InfoLevel)
}

// slogWriter is a zerolog.LevelWriter that re-emits each JSON event as an
// slog record, keeping its message and fields.
type slogWriter struct{ log *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) { return w.WriteLevel(zerolog.InfoLevel, p) }

func (w slogWriter) WriteLevel(l zerolog.Level, p []byte) (int, error) {
	level := slog.LevelInfo
	switch {
	case l >= zerolog.ErrorLevel:
		level = slog.LevelError
	case l == zerolog.WarnLevel:
		level = slog.LevelWarn
	case l <= zerolog.DebugLevel:
		level = slog.LevelDebug
	}
	var fields map[string]any
	if err := json.Unmarshal(p, &fields); err != nil {
		w.log.Log(context.Background(), level, "meowcaller", "raw", string(p))
		return len(p), nil
	}
	msg, _ := fields[zerolog.MessageFieldName].(string)
	delete(fields, zerolog.MessageFieldName)
	delete(fields, zerolog.LevelFieldName)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := make([]slog.Attr, 0, len(keys))
	for _, k := range keys {
		attrs = append(attrs, slog.Any(k, fields[k]))
	}
	w.log.LogAttrs(context.Background(), level, "meowcaller: "+msg, attrs...)
	return len(p), nil
}
