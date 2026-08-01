package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/moutansos/op/internal/domain"
)

// Run starts the dashboard in the terminal's alternate screen.
func Run(ctx context.Context, service domain.Service, options Options) error {
	return run(ctx, service, options, tea.WithAltScreen())
}

func run(ctx context.Context, service domain.Service, options Options, programOptions ...tea.ProgramOption) error {
	if service == nil {
		return errors.New("tui: service is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	model := NewModel(ctx, service, options)
	defer model.cancel()
	programOptions = append([]tea.ProgramOption{tea.WithContext(ctx)}, programOptions...)
	_, err := tea.NewProgram(model, programOptions...).Run()
	return err
}
