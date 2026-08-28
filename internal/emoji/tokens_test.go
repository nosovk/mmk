package emoji

import (
	"reflect"
	"testing"
)

// text builds a TokenText for table-driven test brevity.
func text(s string) Token { return Token{Kind: TokenText, Text: s} }

// emoji builds a TokenEmoji for table-driven test brevity.
func emoji(plain, url string) Token { return Token{Kind: TokenEmoji, Text: plain, URL: url} }

func TestResolveEmojiToTokens_Trivial(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Token
	}{
		{"empty", "", nil},
		{"ascii only", "hello world", []Token{text("hello world")}},
		{"only spaces", "   ", []Token{text("   ")}},
		{"newlines preserved", "line one\nline two", []Token{text("line one\nline two")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveEmojiToTokens(c.in, nil)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ResolveEmojiToTokens(%q) = %#v\n want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestResolveEmojiToTokens_Shortcodes(t *testing.T) {
	customParrot := "https://emoji.example.com/party_parrot.gif"
	customs := map[string]string{
		"party_parrot": customParrot,
	}

	cases := []struct {
		name string
		in   string
		want []Token
	}{
		{"provider shortcode stays text", ":thumbsup: nice", []Token{text(":thumbsup: nice")}},
		{
			"unknown shortcode passes through as text",
			":not_an_emoji_xyz: hello",
			[]Token{text(":not_an_emoji_xyz: hello")},
		},
		{
			"broken shortcode (missing closing colon)",
			":heart still text",
			[]Token{text(":heart still text")},
		},
		{
			"workspace custom",
			"hello :party_parrot:",
			[]Token{text("hello "), emoji(":party_parrot:", customParrot)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveEmojiToTokens(c.in, customs)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ResolveEmojiToTokens(%q):\n got  %#v\n want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestResolveEmojiToTokens_UnicodeClusters(t *testing.T) {
	thumbURL := CDNBaseURL + "1f44d.png"                   // 👍
	astronautURL := CDNBaseURL + "1f468-200d-1f680.png"    // 👨‍🚀 (ZWJ kept)
	heartURL := CDNBaseURL + "2764-fe0f.png"               // ❤️ (VS16 preserved)
	flagUSURL := CDNBaseURL + "1f1fa-1f1f8.png"            // 🇺🇸 (regional indicators)
	rainbowURL := CDNBaseURL + "1f3f3-fe0f-200d-1f308.png" // 🏳️‍🌈 (VS16 preserved mid-sequence)
	keycapURL := CDNBaseURL + "31-fe0f-20e3.png"           // 1️⃣ (ASCII base preserved)

	cases := []struct {
		name string
		in   string
		want []Token
	}{
		{
			"single emoji",
			"\U0001F44D",
			[]Token{emoji("\U0001F44D", thumbURL)},
		},
		{
			"emoji with text",
			"nice \U0001F44D!",
			[]Token{text("nice "), emoji("\U0001F44D", thumbURL), text("!")},
		},
		{
			"ZWJ sequence stays one token",
			"\U0001F468\u200D\U0001F680",
			[]Token{emoji("\U0001F468\u200D\U0001F680", astronautURL)},
		},
		{
			"VS16 sequence",
			"\u2764\uFE0F",
			[]Token{emoji("\u2764\uFE0F", heartURL)},
		},
		{
			"regional indicator pair",
			"\U0001F1FA\U0001F1F8",
			[]Token{emoji("\U0001F1FA\U0001F1F8", flagUSURL)},
		},
		{
			"rainbow flag (ZWJ + VS16)",
			"\U0001F3F3\uFE0F\u200D\U0001F308",
			[]Token{emoji("\U0001F3F3\uFE0F\u200D\U0001F308", rainbowURL)},
		},
		{
			"ASCII-leading keycap sequence",
			"1\uFE0F\u20E3",
			[]Token{emoji("1\uFE0F\u20E3", keycapURL)},
		},
		{
			"two emoji adjacent",
			"\U0001F44D\u2764\uFE0F",
			[]Token{emoji("\U0001F44D", thumbURL), emoji("\u2764\uFE0F", heartURL)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveEmojiToTokens(c.in, nil)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ResolveEmojiToTokens(%q):\n got  %#v\n want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestResolveEmojiToTokens_Mixed(t *testing.T) {
	thumbURL := CDNBaseURL + "1f44d.png"
	heartURL := CDNBaseURL + "2764-fe0f.png"
	rocketURL := CDNBaseURL + "1f680.png"

	cases := []struct {
		name string
		in   string
		want []Token
	}{
		{"shortcode then unicode emoji", ":thumbsup: yes \u2764\uFE0F", []Token{text(":thumbsup: yes "), emoji("\u2764\uFE0F", heartURL)}},
		{"unicode emoji then shortcode", "\U0001F44D :heart:", []Token{emoji("\U0001F44D", thumbURL), text(" :heart:")}},
		{
			"colon inside non-emoji text (URL-like)",
			"see https://example.com path",
			[]Token{text("see https://example.com path")},
		},
		{
			"lone colon",
			"foo : bar",
			[]Token{text("foo : bar")},
		},
		{
			"empty colon pair",
			"foo :: bar",
			[]Token{text("foo :: bar")},
		},
		{
			"non-emoji unicode passes through",
			"caf\u00e9",
			[]Token{text("caf\u00e9")},
		},
		{"three emoji + text + shortcode at boundaries", "\U0001F44D\u2764\uFE0F\U0001F680 mid :thumbsup:", []Token{emoji("\U0001F44D", thumbURL), emoji("\u2764\uFE0F", heartURL), emoji("\U0001F680", rocketURL), text(" mid :thumbsup:")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveEmojiToTokens(c.in, nil)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ResolveEmojiToTokens(%q):\n got  %#v\n want %#v", c.in, got, c.want)
			}
		})
	}
}
