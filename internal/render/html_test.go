package render

import (
	"regexp"
	"strings"
	"testing"
)

// ansi matches SGR sequences and OSC 8 hyperlink markers so assertions can
// compare plain text regardless of the color profile lipgloss detects in
// the test environment.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m|\x1b\]8;;[^\x1b]*\x1b\\`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

func TestText(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
		want     string
	}{
		{"plain", "hello", "hello"},
		{"entities decoded", "a &amp; b &lt;c&gt;", "a & b <c>"},
		{"bold", "<strong>bold</strong>", "bold"},
		{"nested styles", "<strong>a <em>b</em></strong>", "a b"},
		{"br becomes newline", "one<br>two", "one\ntwo"},
		{"block end becomes newline", "<p>one</p><p>two</p>", "one\ntwo"},
		{"unknown tag degrades to text", "<div><foo>text</foo></div>", "text"},
		{"stray end tag does not panic", "</strong>text", "text"},
		{"trailing whitespace trimmed", "text<br><br>", "text"},

		// A link whose visible text already is the URL needs no repeat.
		{"link text differs from href",
			`<a href="https://example.com/x">click</a>`,
			"click <https://example.com/x>"},
		{"link text equals href",
			`<a href="https://example.com/x">https://example.com/x</a>`,
			"https://example.com/x"},
		{"link without href", `<a>bare</a>`, "bare"},
		// The server's mention markup: the name is the destination, so
		// no visible href.
		{"mention keeps only the name",
			`<a href="//bob.newgrounds.com" title="Check out bob's user page!" target="_blank">@bob</a>`,
			"@bob"},
		{"group mention keeps only the name",
			`<a href="https://www.newgrounds.com" title="Visit Newgrounds!" target="_blank">@!everyone</a>`,
			"@!everyone"},

		{"image with alt and src",
			`<img src="https://example.com/i.png" alt="cat">`,
			"[cat] [image: https://example.com/i.png]"},
		{"image without alt",
			`<img src="https://example.com/i.png">`,
			"[image: https://example.com/i.png]"},

		{"emote from title",
			`<span class="ng-emoticon-ngaHoldup ng-emoticon" title="ngaHoldup"></span>`,
			":ngaHoldup:"},
		{"emote from class",
			`<span class="ng-emoticon-ngaWink ng-emoticon"></span>`,
			":ngaWink:"},
		{"emoticon marker without code renders nothing",
			`<span class="ng-emoticon"></span>`, ""},
		{"non-emote span passes through text",
			`<span class="other">text</span>`, "text"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := plain(Text(tc.fragment)); got != tc.want {
				t.Errorf("Text(%q)\n got %q\nwant %q",
					tc.fragment, got, tc.want)
			}
		})
	}
}

// TestTextStylesAreApplied guards the styling path itself, which the plain
// comparisons above deliberately strip.
func TestTextStylesAreApplied(t *testing.T) {
	got := Text("<strong>bold</strong>")
	if got == "bold" {
		t.Skip("no color profile in this environment; styling is a no-op")
	}
	if plain(got) != "bold" {
		t.Errorf("styled output %q does not reduce to %q", got, "bold")
	}
}

// TestTextHyperlinks checks the OSC 8 wrapping itself, which plain()
// strips: the link text and the visible href both carry the URL.
func TestTextHyperlinks(t *testing.T) {
	got := Text(`<a href="https://example.com/x">click</a>`)
	want := "\x1b]8;;https://example.com/x\x1b\\"
	if strings.Count(got, want) != 2 {
		t.Errorf("Text() = %q, want two hyperlink openers for %q", got, want)
	}
	if strings.Count(got, "\x1b]8;;\x1b\\") != 2 {
		t.Errorf("Text() = %q, want two hyperlink closers", got)
	}
	if Hyperlink("", "x") != "x" {
		t.Error("Hyperlink with no URL should return the text unchanged")
	}
	mention := Text(`<a href="//bob.newgrounds.com">@bob</a>`)
	if !strings.Contains(mention, "\x1b]8;;https://bob.newgrounds.com\x1b\\") {
		t.Errorf("mention = %q, want a scheme added to the scheme-relative href", mention)
	}
}
