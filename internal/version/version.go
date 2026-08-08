// Package version provides formatting helpers for mmk's build-time
// version metadata. The build vars themselves live in package main
// (injected via -ldflags by GoReleaser); callers pass the version
// string in so this package stays free of build-time coupling.
package version

// osc8 wraps url as a clickable OSC-8 hyperlink whose visible label is
// the URL itself. Terminals without OSC-8 support render the label as
// plain text.
func osc8(url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + url + "\x1b]8;;\x1b\\"
}

// ModalFooter returns the single attribution line shown at the bottom
// of the TUI help modal, e.g.:
//
//	mmk dev - Made with ❤️ by Grant Ammons (https://grant.dev)
//
// The URL is OSC-8 wrapped so supporting terminals make it clickable.
func ModalFooter(version string) string {
	return "mmk " + version + " - Made with \u2764\ufe0f by Grant Ammons (" + osc8("https://grant.dev") + ")"
}
