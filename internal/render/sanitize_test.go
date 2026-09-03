package render

import (
	"strings"
	"testing"
)

// TestTextNeutralizesTerminalControls is the terminal boundary: the
// server sanitizes for a browser, so anything a terminal would act on
// must die here, whether it arrives literally, entity-encoded, as a C1
// byte, or inside an attribute.
func TestTextNeutralizesTerminalControls(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
		want     string
	}{
		{"literal CSI", "a\x1b[2Jb", "a[2Jb"},
		{"entity-encoded CSI", "a&#27;[2Jb", "a[2Jb"},
		{"C1 CSI", "a2Jb", "a2Jb"},
		{"OSC ended by BEL", "a\x1b]52;c;xyz\x07b", "a]52;c;xyzb"},
		{"OSC ended by ST", "a\x1b]52;c;xyz\x1b\\b", "a]52;c;xyz\\b"},
		// The tokenizer turns a bare CR into a newline (HTML's line
		// normalization) before Plain sees it; either way it is not a CR.
		{"carriage return", "a\rb", "a\nb"},
		{"bidi override and isolate", "a‮b⁦c", "abc"},
		{"bidi marks", "a؜b‎c‏d", "abcd"},
		{"newline survives", "<pre>a\nb</pre>", "a\nb"},
		{"controls in alt", "<img src=\"https://x/i.png\" alt=\"c\x1b[2Ja\">",
			"[c[2Ja] [image: https://x/i.png]"},
		{"controls in src", "<img src=\"https://x/\x1b[2J.png\">",
			"[image: https://x/[2J.png]"},
		{"controls in emote title",
			"<span class=\"ng-emoticon-x ng-emoticon\" title=\"ok\x1b[2J\"></span>",
			":ok[2J:"},
		{"javascript href is not a link",
			`<a href="javascript:alert(1)">click</a>`, "click"},
		{"mention to a foreign host shows the target",
			`<a href="https://evil.example/">@bob</a>`,
			"@bob <https://evil.example/>"},
		{"group mention to a foreign host shows the target",
			`<a href="https://evil.example/">@!everyone</a>`,
			"@!everyone <https://evil.example/>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Text(tc.fragment)
			if strings.ContainsAny(got, "\x07\r‮⁦؜‎‏") ||
				strings.Contains(plain(got), "\x1b") {
				t.Errorf("Text(%q) = %q still carries a control", tc.fragment, got)
			}
			if p := plain(got); p != tc.want {
				t.Errorf("Text(%q)\n got %q\nwant %q", tc.fragment, p, tc.want)
			}
		})
	}
}

// TestHyperlinkRejectsUnsafeTargets: the href sits inside an escape
// sequence, so a control character in it would end the OSC 8 early and
// leave the rest live on the terminal.
func TestHyperlinkRejectsUnsafeTargets(t *testing.T) {
	// An http URL with controls inside keeps its harmless remainder as
	// the target; what matters is that nothing can end the envelope.
	for _, href := range []string{
		"https://x/\x1b\\\x1b]52;c;xyz\x07",
		"https://x/\x07\x1b]52;c;xyz\x07",
		"https://x/\x9c\x9d52;c;xyz",
	} {
		got := Hyperlink(href, "text")
		if !strings.HasPrefix(got, "\x1b]8;;https://x/") {
			t.Errorf("Hyperlink(%q) = %q, want an https://x/ link", href, got)
		}
		if inner := plain(got); strings.ContainsAny(inner, "\x1b\x07\x9c\x9d") {
			t.Errorf("Hyperlink(%q) = %q leaks a control past the envelope",
				href, got)
		}
	}
	for _, href := range []string{
		"javascript:alert(1)",
		"data:text/html,hi",
		"mailto:a@b",
		"not a url",
		"",
	} {
		if got := Hyperlink(href, "text"); got != "text" {
			t.Errorf("Hyperlink(%q) = %q, want plain text", href, got)
		}
	}
	got := Hyperlink("//bob.newgrounds.com", "x")
	if !strings.HasPrefix(got, "\x1b]8;;https://bob.newgrounds.com\x1b\\") {
		t.Errorf("scheme-relative href = %q", got)
	}
}

func TestPlainAndLine(t *testing.T) {
	in := "a\x1b[2Jb\nc\td\r"
	if got, want := Plain(in), "a[2Jb\nc\td"; got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
	if got, want := Line(in), "a[2Jb c d"; got != want {
		t.Errorf("Line = %q, want %q", got, want)
	}
}
