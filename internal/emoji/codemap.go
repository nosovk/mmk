package emoji

// CodeMap returns the bundled shortcode-to-glyph map. Standard shortcode
// aliases were provider-specific and are no longer bundled.
func CodeMap() map[string]string { return nil }

// Sprint preserves provider-owned :shortcode: sequences and other text.
func Sprint(s string) string {
	return shortcodeRe.ReplaceAllStringFunc(s, func(match string) string {
		return match
	})
}
