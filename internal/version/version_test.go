package version

import "testing"

func TestSatisfies(t *testing.T) {
	cases := []struct {
		have, need string
		want       bool
	}{
		{"v1.2.3", "1.2.3", true},
		{"v1.2.3", "v1.2.4", false},
		{"v1.10.0", "1.9.9", true},
		{"v2.0.0", "1.99.0", true},
		{"v1.2.3", "1.3.0", false},
		{"dev", "9.0.0", true},
		{"v0.0.0-20261123000000-abcdef123456", "9.0.0", true},
		{"v1.2.4-0.20261123000000-abcdef123456", "1.2.4", true},
		{"v1.2.4-0.20261123000000-abcdef123456", "1.2.5", false},
		{"v1.2.3", "not-a-version", false},
	}
	for _, c := range cases {
		if got := Satisfies(c.have, c.need); got != c.want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
	if !Valid("1.2.3") || !Valid("v1.2.3") || Valid("1.2") || Valid("1.2.3-beta") {
		t.Error("Valid is wrong")
	}
}
