package shape

import (
	"strings"
	"testing"
)

// A negative or tiny limit must not panic.
func TestCutWordsSmallLimits(t *testing.T) {
	for _, n := range []int{-100, -2, -1, 0, 1, 2} {
		got := CutWords("alpha beta gamma", n)
		if got != "(long value left out)" {
			t.Errorf("CutWords(n=%d) = %q", n, got)
		}
	}
	if got := CutWords("alpha beta gamma", 8); got != "alpha ..." {
		t.Errorf("CutWords(n=8) = %q", got)
	}
	if got := CutWords("", -1); got != "" {
		t.Errorf("an empty string stays empty: %q", got)
	}
	if got := CutWords(strings.Repeat("x", 3), 3); got != "xxx" {
		t.Errorf("a string of exactly n bytes is kept: %q", got)
	}
}
