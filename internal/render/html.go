// Package render converts NG Chat's server-rendered message HTML into
// ANSI-styled terminal text. The server ships finished HTML (the raw text
// is also on the wire, but unformatted), so a terminal client renders by
// translating tags rather than reimplementing the site's formatter.
// Unknown tags degrade to their text content.
package render

import (
	"strings"

	"charm.land/lipgloss/v2"
	"golang.org/x/net/html"
)

var (
	boldStyle   = lipgloss.NewStyle().Bold(true)
	italicStyle = lipgloss.NewStyle().Italic(true)
	strikeStyle = lipgloss.NewStyle().Strikethrough(true)
	codeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	linkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).
			Underline(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
	emoteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
)

// styleState tracks which inline styles are open at the current token.
type styleState struct {
	bold, italic, strike, code int
}

// apply renders text with whatever styles are currently open.
func (s styleState) apply(text string) string {
	st := lipgloss.NewStyle()
	if s.bold > 0 {
		st = st.Bold(true)
	}
	if s.italic > 0 {
		st = st.Italic(true)
	}
	if s.strike > 0 {
		st = st.Strikethrough(true)
	}
	if s.code > 0 {
		st = st.Foreground(lipgloss.Color("11"))
	}
	return st.Render(text)
}

// link tracks an open <a> so the href can be checked against the visible
// text once it is complete.
type link struct {
	href string
	text strings.Builder
}

// Hyperlink wraps text in an OSC 8 hyperlink so terminals that support it
// (kitty, WezTerm, Ghostty, iTerm2, recent GNOME and Windows terminals)
// make it clickable. Terminals that do not simply show the text; the
// sequence is zero-width for lipgloss, so wrapping and padding stay
// correct either way. A url that is not http(s) (see safeHref) returns
// text unchanged: the target sits inside an escape sequence, so it is
// the one place a control character could end the sequence early.
func Hyperlink(url, text string) string {
	url = safeHref(url)
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// Text renders one message's HTML fragment to ANSI terminal text. Every
// piece of it, text and attribute alike, passes through Plain first: the
// tokenizer decodes entities, so "&#27;" is a live ESC by the time it is
// text.
//
// Links become OSC 8 hyperlinks (see Hyperlink). Emote sprites
// (<span class="ng-emoticon-...">) become ":code:" — the classes reference
// site CSS sprites, not image URLs, so until the image pipeline lands the
// code itself is the most faithful rendering.
func Text(fragment string) string {
	tok := html.NewTokenizer(strings.NewReader(fragment))
	var out strings.Builder
	var state styleState
	var links []*link

	appendText := func(text string) {
		if len(links) > 0 {
			l := links[len(links)-1]
			l.text.WriteString(text)
			out.WriteString(Hyperlink(l.href, linkStyle.Render(text)))
			return
		}
		out.WriteString(state.apply(text))
	}

	for {
		tt := tok.Next()
		if tt == html.ErrorToken {
			break
		}
		t := tok.Token()
		switch tt {
		case html.TextToken:
			appendText(Plain(t.Data))
		case html.SelfClosingTagToken, html.StartTagToken:
			switch t.Data {
			case "br":
				out.WriteString("\n")
			case "strong", "b":
				state.bold++
			case "em", "i":
				state.italic++
			case "del", "s", "strike":
				state.strike++
			case "code", "pre":
				state.code++
			case "a":
				links = append(links, &link{href: safeHref(attr(t, "href"))})
			case "img":
				src := Line(attr(t, "src"))
				// The separator is conditional: without alt text an
				// image-only message would start with a stray space.
				sep := ""
				if alt := Line(attr(t, "alt")); alt != "" {
					appendText("[" + alt + "]")
					sep = " "
				}
				if src != "" {
					out.WriteString(dimStyle.Render(sep + "[image: " + src + "]"))
				}
			case "span":
				if code := emoteCode(t); code != "" {
					out.WriteString(emoteStyle.Render(":" + code + ":"))
				}
			}
		case html.EndTagToken:
			switch t.Data {
			case "strong", "b":
				state.bold = max(0, state.bold-1)
			case "em", "i":
				state.italic = max(0, state.italic-1)
			case "del", "s", "strike":
				state.strike = max(0, state.strike-1)
			case "code", "pre":
				state.code = max(0, state.code-1)
			case "p", "h1", "h2", "h3", "h4", "blockquote", "li":
				out.WriteString("\n")
			case "a":
				if len(links) > 0 {
					l := links[len(links)-1]
					links = links[:len(links)-1]
					// The text is already an OSC 8 hyperlink, so the href
					// is not repeated after it: a moderator's Markdown
					// link reads as its label, the way it does on the web,
					// at the cost of terminals without OSC 8 (macOS
					// Terminal.app) having no way to reach it. A mention
					// is the one place the target is still shown: "@bob"
					// claims a destination, so an href that is not bob's
					// page (mentionTarget) is spelled out rather than
					// letting the name vouch for a link it does not own.
					text := strings.TrimSpace(l.text.String())
					if l.href != "" && strings.HasPrefix(text, "@") &&
						!mentionTarget(text, l.href) {
						out.WriteString(Hyperlink(l.href,
							dimStyle.Render(" <"+l.href+">")))
					}
				}
			}
		}
	}
	return strings.TrimRight(out.String(), "\n \t")
}

// attr fetches an attribute value from a token.
func attr(t html.Token, name string) string {
	for _, a := range t.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// emoteCode extracts the sprite short-code from an emoticon span, whose
// title attribute carries it (class "ng-emoticon-<code> ng-emoticon").
func emoteCode(t html.Token) string {
	if !strings.Contains(attr(t, "class"), "ng-emoticon") {
		return ""
	}
	if title := Line(attr(t, "title")); title != "" {
		return title
	}
	for _, cls := range strings.Fields(Line(attr(t, "class"))) {
		if code, ok := strings.CutPrefix(cls, "ng-emoticon-"); ok && code != "" {
			return code
		}
	}
	return ""
}
