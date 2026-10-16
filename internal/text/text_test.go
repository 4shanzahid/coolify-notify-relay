package text

import "testing"

func TestIsSHA(t *testing.T) {
	for s, want := range map[string]bool{
		"3f2a9c1e7b4d8a6f0c5e2b9d1a7f4c8e6b3d0a5f": true,
		"3F2A9C1E7B4D8A6F0C5E2B9D1A7F4C8E6B3D0A5F": false,
		"3f2a9c1": false,
		"HEAD":    false,
		"":        false,
	} {
		if got := IsSHA(s); got != want {
			t.Errorf("IsSHA(%q) = %v", s, got)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 6, "hello…"},
		{"ünïcödé", 4, "ünï…"},
		{"abc", 1, "a"},
		{"abc", 0, ""},
	}
	for _, c := range cases {
		if got := Truncate(c.in, c.n); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
