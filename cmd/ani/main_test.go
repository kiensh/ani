package main

import "testing"

func TestMovieArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want cliArgs
	}{
		{[]string{"--movies"}, cliArgs{Movies: true}},
		{[]string{"Breaking", "--movies", "Bad", "--dry-run"}, cliArgs{Movies: true, DryRun: true, Query: "Breaking Bad"}},
		{[]string{"--movies", "1917"}, cliArgs{Movies: true, Query: "1917"}},
	} {
		if got := parseArgs(tc.args); got != tc.want {
			t.Errorf("%v: got %+v, want %+v", tc.args, got, tc.want)
		}
	}
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		debug  bool
		dryRun bool
		help   bool
		query  string
	}{
		{"flags after positional", []string{"frieren", "--dry-run", "--debug"}, true, true, false, "frieren"},
		{"flags before positional", []string{"--debug", "--dry-run", "frieren"}, true, true, false, "frieren"},
		{"flags around positional", []string{"--debug", "frieren", "--dry-run"}, true, true, false, "frieren"},
		{"positional only", []string{"frieren"}, false, false, false, "frieren"},
		{"debug only", []string{"frieren", "--debug"}, true, false, false, "frieren"},
		{"dry-run only", []string{"frieren", "--dry-run"}, false, true, false, "frieren"},
		{"short flag forms", []string{"-debug", "-dry-run", "x"}, true, true, false, "x"},
		{"help", []string{"-h"}, false, false, true, ""},
		{"help long", []string{"--help"}, false, false, true, ""},
		{"no args", nil, false, false, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseArgs(c.args)
			want := cliArgs{Debug: c.debug, DryRun: c.dryRun, Help: c.help, Query: c.query}
			if got != want {
				t.Errorf("parseArgs(%v) = %+v, want %+v", c.args, got, want)
			}
		})
	}
}
