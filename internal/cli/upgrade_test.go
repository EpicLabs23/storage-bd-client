package cli

import "testing"

func TestIsDevBuild(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.1.0":                  false,
		"v0.1.0-rc1":              false,
		"dev":                     true,
		"abc1234":                 true,
		"v0.1.0-3-gabc1234":       true,
		"v0.1.0-3-gabc1234-dirty": true,
		"v0.1.0-dirty":            true,
		"v0.1.0-rc1-2-g0123abcd":  true,
	} {
		if got := isDevBuild(v); got != want {
			t.Errorf("isDevBuild(%q) = %v, want %v", v, got, want)
		}
	}
}
