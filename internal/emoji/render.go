package emoji

import "regexp"

// shortcodeRe matches colon-delimited emoji shortcodes embedded in text:
// a colon, a name made of letters/digits/_/+/-, then a closing colon.
// Anchored by the colons; non-greedy isn't needed because the inner
// class disallows colons.
var shortcodeRe = regexp.MustCompile(`:[A-Za-z0-9_+\-]+:`)

// ResolveShortcodesInText preserves provider-owned shortcodes verbatim.
func ResolveShortcodesInText(s string) string {
	return s
}
