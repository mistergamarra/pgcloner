// Package uiselect wraps charmbracelet/huh to give every interactive step
// in pgcloner the same look and feel: searchable single-select,
// multi-select, confirm, and text input, each cancellable with Esc/Ctrl-C.
package uiselect

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
)

// ErrBack is returned when the user cancels a step (Esc/Ctrl-C). Callers
// use it to step back to the previous stage of a wizard.
var ErrBack = errors.New("uiselect: user went back")

// One prompts the user to pick a single option from choices.
func One(title string, choices []string) (string, error) {
	if len(choices) == 0 {
		return "", fmt.Errorf("uiselect: no choices for %q", title)
	}
	var picked string
	opts := make([]huh.Option[string], len(choices))
	for i, c := range choices {
		opts[i] = huh.NewOption(c, c)
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(title).
			Options(opts...).
			Filtering(true).
			Value(&picked),
	))
	if err := form.Run(); err != nil {
		return "", asBack(err)
	}
	return picked, nil
}

// Many prompts the user to pick zero or more options, pre-selected by
// default (mirrors dump.sh's fzf --bind load:select-all behaviour).
func Many(title string, choices []string) ([]string, error) {
	if len(choices) == 0 {
		return nil, fmt.Errorf("uiselect: no choices for %q", title)
	}
	var picked []string
	opts := make([]huh.Option[string], len(choices))
	for i, c := range choices {
		opts[i] = huh.NewOption(c, c).Selected(true)
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title(title).
			Description("space/x: toggle one  •  ctrl+a: toggle all  •  enter: confirm  •  esc: back\n" +
				"/ to filter, then enter to leave the filter box — space/x types into it until then").
			Options(opts...).
			Filtering(true).
			Value(&picked),
	))
	if err := form.Run(); err != nil {
		return nil, asBack(err)
	}
	return picked, nil
}

// Confirm asks a yes/no question, defaulting to No.
func Confirm(title string) (bool, error) {
	var ok bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(title).
			Value(&ok),
	))
	if err := form.Run(); err != nil {
		return false, asBack(err)
	}
	return ok, nil
}

// Input prompts for a free-text value, using defaultVal when the user
// submits an empty string.
func Input(title, defaultVal string) (string, error) {
	val := defaultVal
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title(title).
			Placeholder(defaultVal).
			Value(&val),
	))
	if err := form.Run(); err != nil {
		return "", asBack(err)
	}
	if val == "" {
		val = defaultVal
	}
	return val, nil
}

func asBack(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrBack
	}
	return err
}
