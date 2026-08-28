package emoji

// standardShortcodes contains provider-neutral canonical names used by the
// autocomplete picker. It intentionally excludes provider aliases such as
// +1 and thumbsup.
var standardShortcodes = map[string]string{
	"100":              "💯",
	"balloon":          "🎈",
	"bell":             "🔔",
	"bulb":             "💡",
	"check_mark":       "✔️",
	"clap":             "👏",
	"eyes":             "👀",
	"fire":             "🔥",
	"grinning":         "😀",
	"heart":            "❤️",
	"heart_eyes":       "😍",
	"joy":              "😂",
	"laughing":         "😆",
	"party_popper":     "🎉",
	"pray":             "🙏",
	"rocket":           "🚀",
	"rofl":             "🤣",
	"rose":             "🌹",
	"sob":              "😭",
	"sparkles":         "✨",
	"star":             "⭐",
	"thinking":         "🤔",
	"wave":             "👋",
	"warning":          "⚠️",
	"white_check_mark": "✅",
}

func standardCodeMap() map[string]string {
	codemap := make(map[string]string, len(standardShortcodes))
	for name, glyph := range standardShortcodes {
		codemap[":"+name+":"] = glyph
	}
	return codemap
}
