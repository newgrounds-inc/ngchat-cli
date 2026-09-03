package splash

import (
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/newgrounds-inc/ngchat-cli/internal/theme"
)

// Palette is the handful of roles an effect paints with, so an effect
// is written once against roles and every theme lights it its own way.
type Palette struct {
	// Ink is the color the art settles on.
	Ink color.Color
	// Hot is the brightest color, used for the instant a pixel appears.
	Hot color.Color
	// Warm sits between Hot and Ink while a pixel cools.
	Warm color.Color
	// Spark is for debris the effect throws off; it never lands on art.
	Spark color.Color
}

// FromTheme maps theme roles onto the palette: the wordmark settles on
// primary, flashes in as body text, cools through warning gold, and
// throws accent sparks.
func FromTheme(t theme.Theme) Palette {
	return Palette{Ink: t.Primary, Hot: t.BaseContent, Warm: t.Warning,
		Spark: t.Accent}
}

// Effect animates an Art. Frame is a pure function of t, so the UI can
// call it at whatever rate it likes and a test can pin any instant.
type Effect interface {
	// Name identifies the effect in help text and future settings.
	Name() string
	// Duration is when Frame stops changing; Frame at or past it must
	// equal Static.
	Duration() time.Duration
	// Frame renders the art at elapsed time t: exactly a.Rows() lines,
	// each a.W cells wide, ANSI-styled.
	Frame(a Art, p Palette, t time.Duration) []string
}

// Static is the art fully drawn in Ink, the frame every effect ends on.
func Static(a Art, p Palette) []string {
	return render(a, func(x, y int) (color.Color, bool) { return p.Ink, true },
		nil)
}

// cell is one terminal cell of a rendered frame.
type cell struct {
	glyph string
	fg    color.Color // nil for an unstyled cell
}

// render draws the art with lit(x, y) deciding whether pixel (x, y) is
// visible yet and in which color, and over(x, row) optionally placing
// a foreground glyph where the art is blank (the beam, sparks). A cell
// takes its color from its top pixel when both are lit, which every
// effect here keeps equal by coloring per column.
func render(a Art, lit func(x, y int) (color.Color, bool),
	over func(x, row int) (cell, bool)) []string {
	lines := make([]string, a.Rows())
	for row := range a.Rows() {
		cells := make([]cell, a.W)
		for x := range a.W {
			top, bot := a.At(x, 2*row), a.At(x, 2*row+1)
			var topC, botC color.Color
			if top {
				topC, top = lit(x, 2*row)
			}
			if bot {
				botC, bot = lit(x, 2*row+1)
			}
			c := cell{glyph: " "}
			switch {
			case top && bot:
				c = cell{"█", topC}
			case top:
				c = cell{"▀", topC}
			case bot:
				c = cell{"▄", botC}
			default:
				if over != nil {
					if o, ok := over(x, row); ok {
						c = o
					}
				}
			}
			cells[x] = c
		}
		lines[row] = join(cells)
	}
	return lines
}

// join styles runs of same-colored cells together, so a frame is a few
// escape sequences per row rather than one per cell.
func join(cells []cell) string {
	var b strings.Builder
	for i := 0; i < len(cells); {
		j := i
		var run strings.Builder
		for j < len(cells) && sameColor(cells[j].fg, cells[i].fg) {
			run.WriteString(cells[j].glyph)
			j++
		}
		if cells[i].fg == nil {
			b.WriteString(run.String())
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(cells[i].fg).
				Render(run.String()))
		}
		i = j
	}
	return b.String()
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return color.RGBAModel.Convert(a) == color.RGBAModel.Convert(b)
}

// mix blends a toward b by t in [0, 1], in sRGB. Linear-light blending
// would be truer, but the ramps here are short and warm-on-warm, where
// the difference is invisible on a terminal.
func mix(a, b color.Color, t float64) color.Color {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	ca := color.RGBAModel.Convert(a).(color.RGBA)
	cb := color.RGBAModel.Convert(b).(color.RGBA)
	lerp := func(x, y uint8) uint8 {
		return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5)
	}
	return color.RGBA{lerp(ca.R, cb.R), lerp(ca.G, cb.G), lerp(ca.B, cb.B), 0xff}
}
