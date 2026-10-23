package main

import "testing"

func TestSplitServers(t *testing.T) {
	cases := map[string][]string{
		"https://a:8090":          {"https://a:8090"},
		"https://a,https://b":     {"https://a", "https://b"},
		" https://a ; https://b ": {"https://a", "https://b"},
		"https://a\t\thttps://b":  {"https://a", "https://b"},
		"":                        {""},
	}
	for in, want := range cases {
		got := splitServers(in)
		if len(got) != len(want) {
			t.Errorf("splitServers(%q)=%v want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("splitServers(%q)=%v want %v", in, got, want)
				break
			}
		}
	}
}
