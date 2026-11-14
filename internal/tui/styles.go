package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Adaptive colors keep controls readable on both light and dark terminals.
// Lip Gloss detects terminal capabilities and leaves redirected output uncolored.
var (
	textStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#27272A", Dark: "#E4E4E7"})
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#52525B", Dark: "#A1A1AA"})
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"}).Bold(true)
	successStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#166534", Dark: "#86EFAC"})
	warningStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#854D0E", Dark: "#FACC15"})
	dangerStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"}).Bold(true)
	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#164E63", Dark: "#ECFEFF"}).
			Background(lipgloss.AdaptiveColor{Light: "#CFFAFE", Dark: "#164E63"}).Bold(true)
)

type noColorKey struct{}

// WithoutColor disables terminal styling for interactive programs in this context.
func WithoutColor(ctx context.Context) context.Context {
	return context.WithValue(ctx, noColorKey{}, true)
}

func configureColors(ctx context.Context) func() {
	if disabled, _ := ctx.Value(noColorKey{}).(bool); disabled {
		previous := lipgloss.ColorProfile()
		lipgloss.SetColorProfile(termenv.Ascii)
		return func() { lipgloss.SetColorProfile(previous) }
	}
	return func() {}
}

func listRow(content string, selected bool) string {
	if selected {
		return selectedStyle.Render("> " + content)
	}
	return textStyle.Render("  " + content)
}

func statusText(status string) string {
	switch status {
	case "active", "healthy":
		return successStyle.Render(status)
	case "creating", "cleaning", "missing", "conflict", "attention_required":
		return warningStyle.Render(status)
	case "error":
		return dangerStyle.Render(status)
	default:
		return mutedStyle.Render(status)
	}
}

// Keep keycaps distinct from their descriptions, including without color.
func helpLine(bindings string) string {
	parts := strings.Split(bindings, " · ")
	for i, part := range parts {
		key, label, _ := strings.Cut(part, " ")
		style := accentStyle
		if key == "x" || key == "z" {
			style = dangerStyle
		}
		parts[i] = style.Render(key) + " " + mutedStyle.Render(label)
	}
	return strings.Join(parts, mutedStyle.Render(" · ")) + "\n"
}
