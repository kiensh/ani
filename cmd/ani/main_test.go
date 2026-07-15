package main

import "testing"

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		debug       bool
		dryRun      bool
		help        bool
		query       string
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
			dbg, dry, help, q := parseArgs(c.args)
			if dbg != c.debug || dry != c.dryRun || help != c.help || q != c.query {
				t.Errorf("parseArgs(%v) = (debug=%v dry=%v help=%v query=%q), want (debug=%v dry=%v help=%v query=%q)",
					c.args, dbg, dry, help, q, c.debug, c.dryRun, c.help, c.query)
			}
		})
	}
}
