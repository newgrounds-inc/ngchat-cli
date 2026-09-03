package theme

import (
	"image/color"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestEveryRoleIsSet: a widget may use any role of any theme without a
// nil check, so a theme with a hole is a bug here, not there.
func TestEveryRoleIsSet(t *testing.T) {
	for _, name := range Names() {
		th, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) failed for a listed name", name)
		}
		v := reflect.ValueOf(th)
		for i := range v.NumField() {
			f := v.Field(i)
			if f.Kind() != reflect.Interface {
				continue
			}
			// lipgloss.Color answers a bad hex with a non-nil NoColor
			// sentinel that renders as the terminal default, so nil
			// alone would let a typo through.
			if f.IsNil() || f.Interface() == (lipgloss.NoColor{}) {
				t.Errorf("theme %q: role %s is unset or a bad hex", name,
					v.Type().Field(i).Name)
			}
		}
	}
}

func TestLookup(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"ngchat", true},
		{"classic", true},
		{"", false},
		{"NGCHAT", false},
		{"solarized", false},
	}
	for _, c := range cases {
		th, ok := Lookup(c.name)
		if ok != c.ok {
			t.Errorf("Lookup(%q) ok=%v, want %v", c.name, ok, c.ok)
		}
		if ok && th.Name != c.name {
			t.Errorf("Lookup(%q).Name=%q", c.name, th.Name)
		}
	}
}

func TestDefaultIsListed(t *testing.T) {
	names := strings.Join(Names(), ",")
	if !strings.Contains(names, Default().Name) {
		t.Errorf("default %q not in Names() %q", Default().Name, names)
	}
	if want := "classic,ngchat"; names != want {
		t.Errorf("Names()=%q, want %q", names, want)
	}
}

// TestUsernameIsNotPrimary: the reason Username is its own role. If a
// theme ever makes them equal on purpose (classic makes it equal to
// Secondary, which is fine), this documents the ngchat one.
func TestUsernameIsNotPrimary(t *testing.T) {
	th := Default()
	if color.RGBAModel.Convert(th.Username) == color.RGBAModel.Convert(th.Primary) {
		t.Error("ngchat username color equals primary; names would read as brand")
	}
}
