package core

import "errors"

// ErrNotFound is returned when an Item id is not known to the store.
var ErrNotFound = errors.New("core: item not found")

// ErrUnsupported is returned when an adapter does not implement the
// capability a write op needs (Sender, Organizer, StatusPublisher, Fetcher).
var ErrUnsupported = errors.New("core: capability not supported")

// ErrTooManyRecipients is returned by Service.Send/Reply when a fan-out
// broadcast (see MultiRecipientSender) names more recipients than the
// adapter's FanoutPolicy.MaxRecipients allows. It is reported before
// anything is sent.
var ErrTooManyRecipients = errors.New("core: too many recipients for one broadcast")

// ErrDestinationExists is returned by Service.Download when destPath
// already exists and DownloadOptions.Force was not set.
var ErrDestinationExists = errors.New("core: destination file already exists")

// ErrAttachmentTooLarge is returned by Service.Download when an
// attachment's actual size exceeds DownloadOptions.MaxBytes (or
// DefaultMaxDownloadBytes when unset).
var ErrAttachmentTooLarge = errors.New("core: attachment exceeds the download size cap")

// ErrSizeMismatch is returned by Service.Download when the number of
// bytes actually downloaded does not match the attachment's declared
// Size (checked only when Size is known, i.e. > 0).
var ErrSizeMismatch = errors.New("core: downloaded size does not match the declared attachment size")
