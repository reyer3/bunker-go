package core

// GenerateAvatarPNGForTest exposes the unexported generateAvatarPNG to
// package core_test, so avatar_test.go can assert Service.Avatar's
// generated-fallback bytes against the same deterministic renderer
// without duplicating its logic.
func GenerateAvatarPNGForTest(channel Channel, displayName string) []byte {
	return generateAvatarPNG(channel, displayName)
}
