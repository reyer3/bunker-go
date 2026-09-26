package mail

// NativeMultiRecipient implements core.MultiRecipientSender: one SMTP
// submission already RCPTs every To/Cc recipient (see Send), so
// core.Service must never fan this out into N single-recipient calls.
// The method is never called; its presence alone is the marker.
func (a *Adapter) NativeMultiRecipient() {}
