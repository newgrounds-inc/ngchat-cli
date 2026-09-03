// Package splash is the opening screen: a piece of pixel art and an
// Effect that draws it in over time, the way Omarchy plays one Terminal
// Text Effects run over its logo. Art and effect are separate seams on
// purpose (ADR 0005): a new logo is a new Art, a new animation is a new
// Effect, and neither knows the theme beyond a Palette of roles.
//
// Pixels are drawn two per terminal row with the half-block glyphs
// (▀ ▄ █), which makes a pixel square on the usual 1:2 cell and gives
// the wordmark its pixelated look on any monospace font.
package splash

// Art is a monochrome bitmap, row-major, origin top-left. It is a value:
// Scale returns a new one and never mutates the receiver.
type Art struct {
	W, H int
	px   []bool
}

// At reports whether pixel (x, y) is lit. Out-of-range is unlit, so
// callers can read the odd last pixel row of an odd-height bitmap.
func (a Art) At(x, y int) bool {
	if x < 0 || y < 0 || x >= a.W || y >= a.H {
		return false
	}
	return a.px[y*a.W+x]
}

// Rows is the terminal rows the art needs: two pixel rows per cell.
func (a Art) Rows() int { return (a.H + 1) / 2 }

// Scale enlarges every pixel to an n×n block. n < 1 is treated as 1.
func (a Art) Scale(n int) Art {
	if n < 1 {
		n = 1
	}
	out := Art{W: a.W * n, H: a.H * n, px: make([]bool, a.W*a.H*n*n)}
	for y := range out.H {
		for x := range out.W {
			out.px[y*out.W+x] = a.At(x/n, y/n)
		}
	}
	return out
}

// maxScale bounds Fit: past 4 the blocks stop reading as a wordmark and
// start reading as a wall, whatever the terminal size.
const maxScale = 4

// Fit returns the art at the largest scale that fits w columns by h
// rows, and false when it does not fit even at scale 1, which is the
// signal to skip the splash rather than clip it.
func Fit(a Art, w, h int) (Art, bool) {
	for n := maxScale; n >= 1; n-- {
		s := a.Scale(n)
		if s.W <= w && s.Rows() <= h {
			return s, true
		}
	}
	return Art{}, false
}

// glyphs is a 5×7 pixel font covering only what the wordmark spells.
// Add letters here when the art needs them; the space is two columns.
var glyphs = map[rune][]string{
	'N': {
		"#...#",
		"##..#",
		"##..#",
		"#.#.#",
		"#..##",
		"#..##",
		"#...#",
	},
	'G': {
		".###.",
		"#...#",
		"#....",
		"#.###",
		"#...#",
		"#...#",
		".###.",
	},
	'C': {
		".###.",
		"#...#",
		"#....",
		"#....",
		"#....",
		"#...#",
		".###.",
	},
	'H': {
		"#...#",
		"#...#",
		"#...#",
		"#####",
		"#...#",
		"#...#",
		"#...#",
	},
	'A': {
		".###.",
		"#...#",
		"#...#",
		"#####",
		"#...#",
		"#...#",
		"#...#",
	},
	'T': {
		"#####",
		"..#..",
		"..#..",
		"..#..",
		"..#..",
		"..#..",
		"..#..",
	},
	' ': {
		"..",
		"..",
		"..",
		"..",
		"..",
		"..",
		"..",
	},
}

const glyphH = 7

// Wordmark is the "NG CHAT" mark at scale 1: 38×7 pixels, 4 rows.
func Wordmark() Art { return text("NG CHAT") }

// text sets a string in the pixel font with a one-column gap between
// glyphs. Letters the font lacks are a programming error, so it panics
// rather than drawing a hole.
func text(s string) Art {
	w := 0
	for i, r := range s {
		g, ok := glyphs[r]
		if !ok {
			panic("splash: no glyph for " + string(r))
		}
		if i > 0 {
			w++
		}
		w += len(g[0])
	}
	a := Art{W: w, H: glyphH, px: make([]bool, w*glyphH)}
	x := 0
	for i, r := range s {
		if i > 0 {
			x++
		}
		g := glyphs[r]
		for y, row := range g {
			for dx, c := range row {
				a.px[y*w+x+dx] = c == '#'
			}
		}
		x += len(g[0])
	}
	return a
}
