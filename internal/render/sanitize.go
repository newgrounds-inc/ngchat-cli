package render

import (
	"net/url"
	"strings"
)

// Plain strips from s every character a terminal would act on rather
// than display: C0 and C1 controls (so ESC, BEL and the raw CSI/OSC
// bytes that start escape sequences), DEL, and every Unicode
// Bidi_Control character (the marks, the embedding/override pairs and
// the isolates) that reorders what follows.
// Newlines and tabs survive because message HTML carries them in <pre>
// blocks. Every string that comes off the network and ends up on the
// terminal goes through here or through Text (which calls it): the
// server escapes HTML for a browser, and a browser sanitizer is not a
// terminal sanitizer.
func Plain(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		case r >= 0x80 && r <= 0x9f:
			return -1
		case r == 0x061c || r == 0x200e || r == 0x200f:
			return -1
		case r >= 0x202a && r <= 0x202e:
			return -1
		case r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}

// Line is Plain for one-line surfaces (usernames, status bar, error
// rows): newlines and tabs become spaces so a value cannot forge a
// second transcript line or push the layout.
func Line(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' {
			return ' '
		}
		return r
	}, Plain(s))
}

// safeHref returns the link target to use for an href, or "" when it is
// not an http(s) URL. The site writes user-page links scheme-relative
// ("//bob.newgrounds.com"); a terminal has no page scheme to inherit, so
// the site's own is assumed. Anything else (javascript:, data:, a
// control-laden string) has no use in a terminal and would only give an
// OSC 8 link a target the visible text does not show.
func safeHref(href string) string {
	href = strings.TrimSpace(Line(href))
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return href
	}
	return ""
}

// mentionTarget reports whether href is where the site sends a mention
// of text: "@bob" goes to bob's user page, and a group mention such as
// "@!everyone" goes to the site itself. Only then can the visible
// destination be dropped; any other target behind an @-name is shown so
// the reader is not clicking blind.
func mentionTarget(text, href string) bool {
	name, ok := strings.CutPrefix(text, "@")
	if !ok || name == "" {
		return false
	}
	u, err := url.Parse(href)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasPrefix(name, "!") {
		return host == "www.newgrounds.com" || host == "newgrounds.com"
	}
	return host == strings.ToLower(name)+".newgrounds.com"
}
