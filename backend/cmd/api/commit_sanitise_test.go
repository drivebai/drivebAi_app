package main

import "testing"

func TestCommitOrDash(t *testing.T) {
	for in, want := range map[string]string{
		"efae64b": "efae64b", "EFAE64B": "EFAE64B", "": "-",
		"not-hex": "-", "abc\ninjected": "-",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": "-",
	} {
		if got := commitOrDash(in); got != want {
			t.Errorf("commitOrDash(%q) = %q, want %q", in, got, want)
		}
	}
}
