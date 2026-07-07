// Package tui is the bubbletea-based terminal UI for ani. It replaces fzf as
// the default interface (the fzf UI remains available via --fzf).
//
// The package exposes two entry points — RunAnimePicker and RunReleasePicker —
// each of which drives one screen to completion and returns a Result. The
// root model is a small state machine that sequences the screens:
//
//	anime picker → release picker → action prompt → (completed prompt)
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"ani/internal/animetosho"
	"ani/internal/mal"
)

// Result is what a TUI screen returns to its caller. Each Run* function fills
// in the fields relevant to its screen; Quit is set when the user backed out.
type Result struct {
	Quit      bool                // user quit without selecting
	Back      bool                // user wants to return to the previous screen
	Anime     *mal.Item           // selected anime (anime picker)
	Release   *animetosho.Release // selected release (release picker)
	Action    string              // "play" or "download" (action prompt)
	Completed bool                // mark MAL completed (completed prompt)

	// Filter preferences from the release picker (persisted across sessions).
	FilterGroup   string
	FilterQuality string
	FilterSort    string
}

// RunAnimePicker launches the TUI for anime selection. Returns the selected
// anime, or a Result with Quit=true when the user cancels. mode + query drive
// the header line (list vs search). When debug is true, diagnostic info is
// written to stderr (the TUI itself always runs normally).
func RunAnimePicker(items []mal.Item, mode AnimeMode, query string, debug bool) (*Result, error) {
	if len(items) == 0 {
		return &Result{Quit: true}, nil
	}
	m := newAnimePicker(items, mode, query, debug)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	if ap, ok := final.(*animePicker); ok {
		return ap.result, nil
	}
	return &Result{Quit: true}, nil
}

// RunReleasePicker launches the TUI for release selection. item provides the
// anime info shown in the header; group/quality/sortName seed the initial
// filter. fetch returns the releases for a given episode (cached + scoped by
// the caller) and is invoked on demand: initially for the default episode, and
// again whenever the user changes the episode filter.
func RunReleasePicker(item *mal.Item, group, quality, sortName string, fetch func(int) []*animetosho.Release, debug bool) (*Result, error) {
	if item == nil || fetch == nil {
		return &Result{Quit: true}, nil
	}
	m := newReleasePicker(item, group, quality, sortName, fetch, debug)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	if rp, ok := final.(*releasePicker); ok {
		rp.result.FilterGroup = rp.filter.Group
		rp.result.FilterQuality = rp.filter.Quality
		rp.result.FilterSort = rp.filter.Sort
		return rp.result, nil
	}
	return &Result{Quit: true}, nil
}
