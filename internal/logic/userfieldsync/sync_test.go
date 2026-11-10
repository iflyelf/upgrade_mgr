package userfieldsync

import (
	"testing"
	"time"
)

func TestParseInterval(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"30m", 30 * time.Minute, false},
		{"6h", 6 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"", 0, true},
		{"abc", 0, true},
		{"0h", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseInterval(tc.in)
		if tc.err {
			if err == nil {
				t.Fatalf("%q 期望报错，实际 %v", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%q = %v, %v；期望 %v", tc.in, got, err, tc.want)
		}
	}
}
