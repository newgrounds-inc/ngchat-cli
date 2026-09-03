// Package theme is the color vocabulary the UI draws with. A Theme names
// colors by role, the same roles the web client's DaisyUI theme block
// declares (base, primary, secondary, accent, neutral, info, success,
// warning, error, plus the chat-specific username), so a theme ported
// from the site's CSS is a copy of numbers, not a redesign. Widgets ask
// for a role, never a color, which is what lets a second theme exist
// without touching them (ADR 0005).
//
// Colors are exact sRGB; Bubble Tea's renderer downsamples them to the
// terminal's profile and drops them under NO_COLOR, so nothing here
// needs to know what the terminal can show.
package theme

import (
	"image/color"
	"sort"

	"charm.land/lipgloss/v2"
)

// Theme is one named palette. Every field is set for every built-in
// theme; a widget may rely on that.
type Theme struct {
	Name string

	// Base100 is the app background, Base200 panels, Base300 borders and
	// hover. The terminal keeps its own background (see ADR 0005), so
	// Base100 is what the splash paints and what text roles are tuned
	// against, not what the chat screen fills.
	Base100, Base200, Base300 color.Color
	// BaseContent is body text on the base surfaces.
	BaseContent color.Color

	// Primary is the brand color; PrimaryContent is text on it. The
	// status bar is primary on primary-content, and the splash wordmark
	// is drawn in primary.
	Primary, PrimaryContent color.Color
	// Secondary is the second warm tone; on the site it carries links,
	// MOTD framing and mod names.
	Secondary, SecondaryContent color.Color
	// Accent is the loud one, reserved for what must stand out: DMs.
	Accent, AccentContent color.Color
	// Neutral is chrome that is neither surface nor brand.
	Neutral, NeutralContent color.Color

	Info, InfoContent       color.Color
	Success, SuccessContent color.Color
	Warning, WarningContent color.Color
	Error, ErrorContent     color.Color

	// Username colors ordinary speakers. The site keeps it outside the
	// DaisyUI block because DaisyUI has no such role; it is a role here
	// because most of the transcript is names and they must not read as
	// the loud brand color.
	Username color.Color
}

var hex = lipgloss.Color

// ngchat mirrors the "ngchat" block in the web client's styles.css.
// The site declares it in oklch; these are the sRGB conversions of
// those values, rounded to the hex the site's own comments cite.
var ngchat = Theme{
	Name:             "ngchat",
	Base100:          hex("#0c0d11"),
	Base200:          hex("#1d1f26"),
	Base300:          hex("#2b2d36"),
	BaseContent:      hex("#dcd6d2"),
	Primary:          hex("#f79d34"),
	PrimaryContent:   hex("#301d0d"),
	Secondary:        hex("#cf8d60"),
	SecondaryContent: hex("#241104"),
	Accent:           hex("#e94646"),
	AccentContent:    hex("#200a08"),
	Neutral:          hex("#2a2e34"),
	NeutralContent:   hex("#e2ddd9"),
	Info:             hex("#00ade4"),
	InfoContent:      hex("#081319"),
	Success:          hex("#43b966"),
	SuccessContent:   hex("#07150a"),
	Warning:          hex("#fab72a"),
	WarningContent:   hex("#241803"),
	Error:            hex("#f53c41"),
	ErrorContent:     hex("#200a08"),
	Username:         hex("#deb866"),
}

// classic mirrors the "classic" block: the legacy chat's gold on
// near-black, where every username was NG gold.
var classic = Theme{
	Name:             "classic",
	Base100:          hex("#1b1717"),
	Base200:          hex("#242020"),
	Base300:          hex("#363232"),
	BaseContent:      hex("#c9bebe"),
	Primary:          hex("#eb7522"),
	PrimaryContent:   hex("#000000"),
	Secondary:        hex("#eeb211"),
	SecondaryContent: hex("#473605"),
	Accent:           hex("#b96f10"),
	AccentContent:    hex("#201200"),
	Neutral:          hex("#363232"),
	NeutralContent:   hex("#e8e8e8"),
	Info:             hex("#8698a2"),
	InfoContent:      hex("#171d21"),
	Success:          hex("#60b136"),
	SuccessContent:   hex("#102408"),
	Warning:          hex("#eeb211"),
	WarningContent:   hex("#473605"),
	Error:            hex("#f74040"),
	ErrorContent:     hex("#330808"),
	Username:         hex("#eeb211"),
}

var builtin = map[string]Theme{
	ngchat.Name:  ngchat,
	classic.Name: classic,
}

// Default is the theme used when none is asked for: the site's own.
func Default() Theme { return ngchat }

// Lookup finds a built-in theme by name. The name is what NGCHAT_THEME
// carries, so an unknown one is the caller's error to report with
// Names, not something to fall back from silently.
func Lookup(name string) (Theme, bool) {
	t, ok := builtin[name]
	return t, ok
}

// Names lists the built-in themes, sorted, for help text and errors.
func Names() []string {
	names := make([]string, 0, len(builtin))
	for name := range builtin {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
