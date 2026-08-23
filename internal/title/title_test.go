package title

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"The Dark Knight", "the dark knight"},
		{"  The   Dark  Knight ", "the dark knight"},
		{"Don't Look Up", "dont look up"},
		{"Don’t Look Up", "dont look up"},
		{"S.W.A.T.", "swat"},
		{"S.W.A.T. Season 2", "swat season 2"},
		{"Spider-Man: No Way Home", "spider man no way home"},
		// A slug can only hold ASCII, so a typographic fraction has to drop
		// out of both sides for "9½ Weeks" to match the slug "9-weeks".
		{"9½ Weeks", "9 weeks"},
		{"the-dark-knight", "the dark knight"},
		{"", ""},
		{"---", ""},
	}
	for _, tc := range tests {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A title and the slug Subscene filed it under must normalise to the same string,
// or an exact-title match can never be recognised as exact.
func TestNormalize_TitleAndSlugAgree(t *testing.T) {
	pairs := [][2]string{
		{"The Dark Knight", "the-dark-knight"},
		{"Don't Look Up", "dont-look-up"},
		{"Amélie", "amélie"},
		{"WALL·E", "wall-e"},
	}
	for _, p := range pairs {
		if Normalize(p[0]) != Normalize(p[1]) {
			t.Errorf("title %q normalises to %q but slug %q normalises to %q",
				p[0], Normalize(p[0]), p[1], Normalize(p[1]))
		}
	}
}

func TestSlugify(t *testing.T) {
	if got := Slugify("The Dark Knight"); got != "the-dark-knight" {
		t.Errorf("Slugify = %q, want %q", got, "the-dark-knight")
	}
	if got := Slugify("S.W.A.T."); got != "swat" {
		t.Errorf("Slugify = %q, want %q", got, "swat")
	}
}

func TestWords(t *testing.T) {
	got := Words("The Dark Knight")
	if strings.Join(got, "|") != "the|dark|knight" {
		t.Errorf("Words = %v", got)
	}
	if Words("  ") != nil {
		t.Error("Words of an empty query should be nil")
	}
}

func TestFromSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"the-dark-knight", "The Dark Knight"},
		{"stonehouse-first-season", "Stonehouse First Season"},
		{"-1952-3", "1952 3"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := FromSlug(tc.in); got != tc.want {
			t.Errorf("FromSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestYearFromSlug(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"the-lion-king-1994", 1994},
		{"dune-2021", 2021},
		{"the-dark-knight", 0},
		{"top-100-1899", 0},   // before cinema: a number, not a year
		{"episode-2-1080", 0}, // a resolution, not a year
		{"", 0},
	}
	for _, tc := range tests {
		if got := YearFromSlug(tc.in); got != tc.want {
			t.Errorf("YearFromSlug(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
