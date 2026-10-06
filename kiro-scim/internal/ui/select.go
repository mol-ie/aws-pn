package ui

import (
	"errors"
	"os"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"
	"golang.org/x/term"
)

// ErrNoTTY is returned when an interactive selection is requested but the
// process is not attached to an interactive terminal.
var ErrNoTTY = errors.New("interactive selection requires a terminal")

// ErrAborted is returned when the operator cancels the interactive prompt
// (for example with Ctrl+C or Esc).
var ErrAborted = errors.New("selection aborted")

// Selector presents a set of options and returns the indexes the operator
// chose. It is an interface so command logic can be tested with a fake.
type Selector interface {
	Select(prompt string, options []string) ([]int, error)
}

// SurveySelector is the real Selector backed by survey/v2's multi-select
// (checkbox) prompt.
type SurveySelector struct{}

// Select presents a checkbox multi-select for options and returns the indexes
// chosen. It returns ErrNoTTY when stdin/stdout are not a terminal and
// ErrAborted when the operator cancels.
func (SurveySelector) Select(prompt string, options []string) ([]int, error) {
	if !interactive() {
		return nil, ErrNoTTY
	}

	var selected []int
	q := &survey.MultiSelect{
		Message: prompt,
		Options: options,
	}
	// PageSize keeps long lists navigable; the prompt still allows selecting
	// across all options.
	err := survey.AskOne(q, &selected, survey.WithPageSize(15))
	if err != nil {
		if errors.Is(err, terminal.InterruptErr) {
			return nil, ErrAborted
		}
		return nil, err
	}
	return selected, nil
}

// interactive reports whether both stdin and stdout are terminals.
func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
