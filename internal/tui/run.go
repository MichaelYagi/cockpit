package tui

import (
	"context"
	"errors"
	"io"

	tea "charm.land/bubbletea/v2"
)

// RunOptions configure the interactive program.
type RunOptions struct {
	Options
	Input  io.Reader // default: stdin
	Output io.Writer // default: stdout
}

// Run starts the interactive TUI and blocks until the user quits or ctx
// is cancelled.
func Run(ctx context.Context, api API, o RunOptions) error {
	opts := []tea.ProgramOption{tea.WithContext(ctx)}
	if o.Input != nil {
		opts = append(opts, tea.WithInput(o.Input))
	}
	if o.Output != nil {
		opts = append(opts, tea.WithOutput(o.Output))
	}
	_, err := tea.NewProgram(New(api, o.Options), opts...).Run()
	if err != nil && ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
		return nil // cancelled by a signal: a normal exit
	}
	return err
}
