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
