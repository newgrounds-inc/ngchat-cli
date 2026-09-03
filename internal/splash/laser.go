package splash

import (
	"image/color"
	"time"
)

// LaserEtch sweeps a beam left to right; pixels ignite white-hot as it
// passes, cool through warm to ink behind it, and a few sparks fly
// ahead of the beam. It is a port in spirit of Terminal Text Effects'
// laseretch, the effect omarchy.org plays over its mark.
//
// Timing is fixed rather than per-column so a wider terminal (a larger
// scale) does not make the splash slower: the beam always crosses in
// Sweep, and Duration is the same for every terminal.
type LaserEtch struct {
	// Sweep is how long the beam takes to cross the art.
	Sweep time.Duration
	// Cool is how long a pixel takes to settle from Hot to Ink.
	Cool time.Duration
}

// DefaultLaser is the tuning the UI uses: about a second and a half in
// total, long enough to read as a reveal and short enough that nobody
// reaches for the skip key.
var DefaultLaser = LaserEtch{Sweep: 1100 * time.Millisecond,
	Cool: 450 * time.Millisecond}

// Name implements Effect.
func (LaserEtch) Name() string { return "laser" }

// Duration implements Effect: the last column is revealed at Sweep and
// settled Cool later.
func (l LaserEtch) Duration() time.Duration { return l.Sweep + l.Cool }

// frameStep is the clock the spark pattern changes on. Sparks are keyed
// to a frame index, not to t itself, so two calls within the same 33 ms
// draw the same sparks and a test can pin a frame.
const frameStep = 33 * time.Millisecond

// Frame implements Effect.
func (l LaserEtch) Frame(a Art, p Palette, t time.Duration) []string {
	if t >= l.Duration() {
		return Static(a, p)
	}
	if t < 0 {
		t = 0
	}
	// The beam column at t. It runs to a.W (one past the art) so the
	// last column is revealed exactly at Sweep.
	beam := int(float64(a.W) * float64(t) / float64(l.Sweep))
	frame := uint64(t / frameStep)
	lit := func(x, y int) (color.Color, bool) {
		revealAt := time.Duration(float64(l.Sweep) * float64(x) / float64(a.W))
		age := t - revealAt
		if age < 0 {
			return nil, false
		}
		c := float64(age) / float64(l.Cool)
		if c < 0.5 {
			return mix(p.Hot, p.Warm, c*2), true
		}
		return mix(p.Warm, p.Ink, (c-0.5)*2), true
	}
	over := func(x, row int) (cell, bool) {
		if beam >= a.W {
			return cell{}, false
		}
		if x == beam {
			return cell{"│", p.Warm}, true
		}
		// Sparks: a sparse, deterministic scatter up to three columns
		// ahead of the beam, redrawn every frame.
		if d := x - beam; d > 0 && d <= 3 && spark(frame, x, row) {
			return cell{"·", p.Spark}, true
		}
		return cell{}, false
	}
	return render(a, lit, over)
}

// spark is a cheap integer hash deciding whether a cell throws a spark
// this frame; about one cell in six ahead of the beam does.
func spark(frame uint64, x, row int) bool {
	h := frame*0x9E3779B97F4A7C15 ^ uint64(x)*0xBF58476D1CE4E5B9 ^
		uint64(row)*0x94D049BB133111EB
	h ^= h >> 31
	h *= 0xBF58476D1CE4E5B9
	h ^= h >> 29
	return h%6 == 0
}
