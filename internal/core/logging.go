package core

import "log/slog"

// LogSinkError logs a Sink write failure that its caller has no way to
// act on (an Adapter's event handler, a fire-and-forget local cache
// write) at error level, with channel/account attributes, so a failed
// store write is never silently lost even though the caller keeps
// running instead of aborting. op names the failed Sink method (e.g.
// "upsert", "mark_read"), so one channel's write path can be grepped out
// of journald.
func LogSinkError(channel Channel, account, op string, err error) {
	slog.Error("sink write failed", "channel", string(channel), "account", account, "op", op, "error", err)
}
