// Package render converts NG Chat's server-rendered message HTML into
// ANSI-styled terminal text. The server ships finished HTML (the raw text
// is also on the wire, but unformatted), so a terminal client renders by
// translating tags rather than reimplementing the site's formatter.
// Unknown tags degrade to their text content.
package render

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
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

// link tracks an open <a> so the href can be appended when the visible
// text differs from it.
type link struct {
	href string
	text strings.Builder
}

// Hyperlink wraps text in an OSC 8 hyperlink so terminals that support it
// (kitty, WezTerm, Ghostty, iTerm2, recent GNOME and Windows terminals)
// make it clickable. Terminals that do not simply show the text; the
// sequence is zero-width for lipgloss, so wrapping and padding stay
// correct either way. An empty url returns text unchanged.
func Hyperlink(url, text string) string {
	if url == "" {
		return text
	}
	// The site writes user-page links scheme-relative ("//bob.newgrounds.com");
	// a terminal has no page scheme to inherit, so pick the site's.
	if strings.HasPrefix(url, "//") {
		url = "https:" + url
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// Text renders one message's HTML fragment to ANSI terminal text.
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
			appendText(t.Data)
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
				links = append(links, &link{href: attr(t, "href")})
			case "img":
				src := attr(t, "src")
				// The separator is conditional: without alt text an
				// image-only message would start with a stray space.
				sep := ""
				if alt := attr(t, "alt"); alt != "" {
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
					// The visible href stays even though the text is
					// already a hyperlink: a terminal without OSC 8
					// support would otherwise show a bare word with no
					// way to reach the URL. A mention is the exception:
					// "@bob" already names where it goes.
					text := strings.TrimSpace(l.text.String())
					if l.href != "" && text != l.href && !strings.HasPrefix(text, "@") {
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
	if title := attr(t, "title"); title != "" {
		return title
	}
	for _, cls := range strings.Fields(attr(t, "class")) {
		if code, ok := strings.CutPrefix(cls, "ng-emoticon-"); ok && code != "" {
			return code
		}
	}
	return ""
}
