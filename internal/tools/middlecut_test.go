package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The cut falls between runes, whatever the width of the characters and
// wherever the limit falls, also in a line with no space.
func TestMiddleCutKeepsRunesWhole(t *testing.T) {
	for _, unit := range []string{"é", "日本", "😀", "aé"} {
		for _, spaced := range []bool{false, true} {
			sep := ""
			if spaced {
				sep = " "
			}
			s := strings.Repeat(unit+sep, 200)
			for n := 24; n < 120; n++ {
				got := middleCut(s, n)
				if !utf8.ValidString(got) {
					t.Fatalf("middleCut(%q..., %d) is not valid UTF-8: %q", unit, n, got)
				}
				if len(got) > n+8 {
					t.Fatalf("middleCut(%q..., %d) is %d bytes", unit, n, len(got))
				}
			}
		}
	}
	if got := middleCut("short", 40); got != "short" {
		t.Errorf("a short line is kept: %q", got)
	}
}
