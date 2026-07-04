package tui

import "testing"

func TestFmtBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0B"}, {512, "512B"}, {512 << 10, "512K"}, {936 << 20, "936M"},
		{24106127360, "22.5G"}, {32 << 30, "32.0G"},
	}
	for _, c := range cases {
		if got := FmtBytes(c.in); got != c.want {
			t.Errorf("FmtBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFmtMemPair(t *testing.T) {
	if got := FmtMemPair(24106127360, 32<<30); got != "22.5/32.0G" {
		t.Errorf("FmtMemPair = %q", got)
	}
}
