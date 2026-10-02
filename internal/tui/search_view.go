package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) renderSearchOverlay() string {
	list := m.visibleSearchEntries()
	rowW := max(1, min(100, m.width-4))
	title := "wyrm — search " + m.searchScope
	query := "/" + m.searchQuery + "_"
	footer := fmt.Sprintf("%d/%d results · ↑↓ move · enter focus · esc cancel · ^f panel / ^z all", len(list), len(m.searchEntries))
	if m.width < 6 || m.height < 6 {
		lines := []string{title, query, "esc cancel"}
		for i := range lines {
			lines[i] = truncate(lines[i], max(1, m.width))
		}
		return strings.Join(lines[:min(len(lines), max(0, m.height))], "\n")
	}
	start, end := viewport(m.searchCur, len(list), m.height-5)
	var lines []string
	switch {
	case m.searchLoading:
		lines = append(lines, hintStyle.Render(truncate("loading entries…", rowW)))
	case m.searchResolving:
		lines = append(lines, hintStyle.Render(truncate("opening result…", rowW)))
	case len(list) == 0:
		lines = append(lines, hintStyle.Render(truncate("no matching results", rowW)))
	default:
		for i := start; i < end; i++ {
			lines = append(lines, renderRow([]span{plain(searchLabel(list[i].label))}, rowW, selectedRow, i == m.searchCur))
		}
	}
	box := focusedBorder.Padding(0, 1).Render(lipgloss.JoinVertical(lipgloss.Left,
		focusedTitle.Render(truncate(title, rowW)), filterStyle.Render(truncate(query, rowW)),
		lipgloss.JoinVertical(lipgloss.Left, lines...), hintStyle.Render(truncate(footer, rowW))))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
