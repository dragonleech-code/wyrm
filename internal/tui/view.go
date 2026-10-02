package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jskoll/wyrm/internal/agent"
)

// Layout constants. The left column holds the four stacked list panels; the
// rest of the width is the preview. Each bordered box costs 2 columns/rows of
// border plus one interior title row.
const (
	minLeftWidth = 24
	maxLeftWidth = 40
	helpHeight   = 1
	borderSize   = 2 // left+right or top+bottom border
	titleRows    = 1

	// minPanelHeight is the smallest a list panel can be and still show
	// anything: both borders, its title, and one row.
	minPanelHeight = borderSize + titleRows + 1
)

// minWidth is the narrowest terminal the layout fits in.
//
// minHeight depends on how many panels are shown, so it is a method: below it
// the panels would render more lines than the screen has — lipgloss's Height()
// pads but never clips — and Bubble Tea keeps only the last screenful, silently
// slicing the top panels away. The compact picker shows two panels and so fits
// in terminals the full four-panel view refuses.
var minWidth = minLeftWidth + 20

func (m Model) minHeight() int {
	return len(m.panels())*minPanelHeight + helpHeight
}

// span is one styled run of text within a list row. Rows are assembled as
// spans rather than as pre-rendered strings so the selection highlight can be
// folded into each run's own style — see renderRow.
type span struct {
	style lipgloss.Style
	text  string
}

func plain(text string) span { return span{text: text} }

// renderRow lays a row's spans into exactly w columns, folding the selection
// style into each span rather than wrapping the finished string.
//
// Wrapping is what the obvious implementation does and it does not work:
// lipgloss renders every styled run with a trailing *full* SGR reset (termenv's
// Style.Styled always appends ESC[0m) and does not re-apply the outer style
// afterward. An outer reverse-video wrap therefore switches itself off at the
// first colored span — which left the window index highlighted and the window
// name plain, on every row of the Windows and Panes panels.
func renderRow(spans []span, w int, sel lipgloss.Style, selected bool) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, s := range spans {
		if used >= w {
			break
		}
		text := s.text
		if used+ansi.StringWidth(text) > w {
			text = ansi.Truncate(text, w-used, "…")
		}
		used += ansi.StringWidth(text)
		style := s.style
		if selected {
			style = style.Inherit(sel)
		}
		b.WriteString(style.Render(text))
	}
	if used < w {
		pad := strings.Repeat(" ", w-used)
		if selected {
			b.WriteString(sel.Render(pad))
		} else {
			b.WriteString(pad)
		}
	}
	return b.String()
}

// View renders the whole TUI frame.
func (m Model) View() string {
	// The help overlay is checked before the size guard: it scrolls and
	// truncates to whatever room it has, so it stays readable in a terminal
	// too small for the four-panel layout — which is exactly where someone is
	// most likely to be looking for the key that gets them out.
	if m.ready && m.mode == modeHelp {
		return m.renderHelpOverlay()
	}
	if m.ready && m.mode == modeFindPane {
		return m.renderFindPaneOverlay()
	}
	if m.ready && m.mode == modeSearch {
		return m.renderSearchOverlay()
	}
	if m.ready && m.mode == modePager {
		return m.renderPagerOverlay()
	}
	if m.ready && m.mode == modeMoveWindow {
		return m.renderMoveWindowOverlay()
	}

	if !m.ready || m.width < minWidth || m.height < m.minHeight() {
		return fmt.Sprintf("wyrm: terminal too small (need at least %dx%d, have %dx%d)",
			minWidth, m.minHeight(), m.width, m.height)
	}

	// One description of the layout, read by both the renderer and the mouse
	// hit test — see layout.go.
	g := m.geometry()

	boxes := make([]string, len(g.panels))
	for i, p := range g.panels {
		boxes[i] = m.renderPanel(p, g.leftW, g.heights[i])
	}
	left := lipgloss.JoinVertical(lipgloss.Left, boxes...)
	right := m.renderPreview(g.rightW, g.bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	frame := lipgloss.JoinVertical(lipgloss.Left, body, m.renderHelp())

	if m.mode == modeMenu && len(m.menu) > 0 {
		x, y, _, _ := m.menuBox()
		frame = overlay(frame, m.renderMenu(), x, y)
	}
	return frame
}

// panelHeights distributes the body's rows across the left panels. Each gets at
// least minPanelHeight, no more than its own fair share while it has content to
// show, and all remaining slack goes to the panel at index slack.
//
// Splitting into equal shares — the previous behavior — gave the Panes panel
// (typically one to three entries) exactly as much room as Sessions, and at
// 80x24 left every list showing two rows at a time.
//
// counts is indexed by position in the shown panel list, not by panel value, so
// the compact two-panel layout needs no special case here.
func panelHeights(bodyH int, counts []int, slack int) []int {
	n := len(counts)
	heights := make([]int, n)
	if n == 0 {
		return heights
	}
	if slack < 0 || slack >= n {
		slack = 0
	}
	if bodyH < n*minPanelHeight {
		// View refuses to render this small; stay total-preserving anyway so
		// nothing overflows if that guard is ever loosened.
		base := bodyH / n
		for i := range heights {
			heights[i] = base
		}
		heights[n-1] += bodyH - base*n
		return heights
	}

	left := bodyH
	for i := range heights {
		heights[i] = minPanelHeight
		left -= minPanelHeight
	}
	fair := bodyH / n
	for i, c := range counts {
		want := borderSize + titleRows + c
		if want > fair {
			want = fair
		}
		grow := want - heights[i]
		if grow > left {
			grow = left
		}
		if grow > 0 {
			heights[i] += grow
			left -= grow
		}
	}
	heights[slack] += left
	return heights
}

// renderPanel draws p, taking its title, rows and empty-state hint from the
// panel table — so View walks whatever panel set the model shows without
// knowing which is which, and a new panel needs no case here.
func (m Model) renderPanel(p panel, outerW, outerH int) string {
	spec := p.spec()
	if spec.rows == nil {
		return ""
	}
	return m.renderListBox(p, spec.title, spec.rows(m), m.cur[p], outerW, outerH, spec.empty)
}

func projectRows(m Model) [][]span {
	projects := m.visibleProjects()
	rows := make([][]span, len(projects))
	for i, p := range projects {
		mark := plain(" ")
		if p.Running {
			mark = span{activeMark, "●"}
		}
		rows[i] = []span{mark, plain(" " + p.Name)}
		switch {
		case p.Wildcard:
			// Distinguishes a directory synthesized from a [[wildcard]]
			// pattern match from a project with its own config file — many
			// of these can appear at once from a single settings entry.
			rows[i] = append(rows[i], span{hintStyle, " ~"})
		case p.Zoxide:
			// A directory zoxide knows about with no wyrm config of its
			// own — distinct from "~" since starting one uses the default
			// config rather than a template.
			rows[i] = append(rows[i], span{hintStyle, " z"})
		}
		// A project's marker is its session's: the project row is the only
		// place a not-currently-selected session's agent shows up at all.
		if p.Running {
			rows[i] = appendAgentMark(rows[i], m.agents.session(p.SessionID))
		}
	}
	return rows
}

func sessionRows(m Model) [][]span {
	entries := m.visibleSessions()
	rows := make([][]span, len(entries))
	for i, e := range entries {
		if !e.Running {
			// A stopped row (allSessions only) has no windows to count and no
			// agents to mark. "stopped" is spelled out rather than left as a
			// missing dot: the difference between this row and the one above
			// it is that Enter starts it, and that is worth a word.
			rows[i] = []span{plain("  " + e.Name + " "), {hintStyle, "stopped"}}
			continue
		}
		mark := plain(" ")
		if e.Session.Attached {
			mark = span{activeMark, "●"}
		}
		rows[i] = appendAgentMark([]span{
			mark,
			plain(" " + e.Name + " "),
			{hintStyle, fmt.Sprintf("(%dw)", e.Session.Windows)},
		}, m.agents.session(e.Session.ID))
	}
	return rows
}

func windowRows(m Model) [][]span {
	windows := m.visibleWindows()
	rows := make([][]span, len(windows))
	for i, w := range windows {
		name := w.Name
		if name == "" {
			name = fmt.Sprintf("window %d", w.Index)
		}
		rows[i] = appendAgentMark([]span{
			{indexMark, fmt.Sprintf("%d:", w.Index)},
			plain(" " + name),
		}, m.agents.window(w.ID))
	}
	return rows
}

func paneRows(m Model) [][]span {
	panes := m.visiblePanes()
	rows := make([][]span, len(panes))
	for i, p := range panes {
		rows[i] = appendAgentMark([]span{
			{indexMark, p.ID},
			plain(" " + p.Command),
		}, m.agents.pane(p.ID))
	}
	return rows
}

// appendAgentMark adds the trailing "waiting for you" glyph to a row, if the
// state warrants one. It trails the text rather than leading it so it can't be
// confused with the running/attached dot in the first column.
func appendAgentMark(row []span, state agent.State) []span {
	if mark, ok := agentMark(state); ok {
		return append(row, mark)
	}
	return row
}

// renderListBox draws one bordered list box with a title, a cursor-tracking
// viewport, and an empty-state hint.
func (m Model) renderListBox(p panel, title string, rows [][]span, cursor, outerW, outerH int, empty string) string {
	focused := m.focus == p
	innerW := outerW - borderSize
	innerH := outerH - borderSize
	listH := innerH - titleRows
	if listH < 1 {
		listH = 1
	}

	// A filter only ever applies to the focused panel, so the focused panel
	// switches to the filter accent while one is active — the same signal
	// lazygit gives a view it's searching in.
	box, titleStyle := blurredBorder, blurredTitle
	if focused {
		box, titleStyle = focusedBorder, focusedTitle
		if m.filtering || m.filter != "" {
			box, titleStyle = filterBorder, filterTitle
		}
	}
	// Show where the viewport sits when the list is taller than the panel, so
	// scrolling isn't invisible.
	label := title
	if len(rows) > listH {
		label = fmt.Sprintf("%s %d/%d", title, cursor+1, len(rows))
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(truncate(label, innerW)))
	b.WriteByte('\n')

	if len(rows) == 0 {
		b.WriteString(hintStyle.Render(truncate(empty, innerW)))
		for i := 1; i < listH; i++ {
			b.WriteByte('\n')
		}
	} else {
		selStyle := trailRow
		if focused {
			selStyle = selectedRow
		}
		start, end := viewport(cursor, len(rows), listH)
		for i := start; i < end; i++ {
			b.WriteString(renderRow(rows[i], innerW, selStyle, i == cursor))
			if i < end-1 {
				b.WriteByte('\n')
			}
		}
		for i := end - start; i < listH; i++ {
			b.WriteByte('\n')
		}
	}

	// MaxHeight as well as Height: Height only pads, so without the cap a box
	// whose content overran its budget would push the layout off-screen.
	return box.Width(innerW).Height(innerH).MaxHeight(outerH).Render(b.String())
}

func (m Model) renderPreview(outerW, outerH int) string {
	innerW := outerW - borderSize
	innerH := outerH - borderSize
	bodyH := innerH - titleRows
	if bodyH < 1 {
		bodyH = 1
	}

	title := m.previewTitle
	if title == "" {
		title = "Preview"
	}

	// A live pane capture gets an accent title so it reads as "live"; a static
	// config preview stays dim.
	titleStyle := blurredTitle
	if m.previewSrc == previewPane {
		titleStyle = focusedTitle
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(truncate(title, innerW)))
	b.WriteByte('\n')

	content := m.preview
	// Pane captures carry SGR escapes (capture-pane -e); reset after every line
	// so an unterminated color can't bleed into the padding or the border.
	colored := m.previewSrc == previewPane
	lines := strings.Split(content, "\n")
	if len(lines) > bodyH {
		// Keep the *end* of a pane capture. capture-pane returns the pane's
		// visible region top-to-bottom, so taking the first rows showed the
		// oldest scrollback and permanently hid the prompt and latest output —
		// the part a live preview exists to show. A config preview reads from
		// the top, so only trim the tail there.
		if colored {
			lines = lines[len(lines)-bodyH:]
		} else {
			lines = lines[:bodyH]
		}
	}
	for i := 0; i < bodyH; i++ {
		if i < len(lines) {
			b.WriteString(truncate(lines[i], innerW))
			if colored {
				b.WriteString(ansi.ResetStyle)
			}
		}
		if i < bodyH-1 {
			b.WriteByte('\n')
		}
	}

	return blurredBorder.Width(innerW).Height(innerH).MaxHeight(outerH).Render(b.String())
}

func (m Model) renderHelp() string {
	switch m.mode {
	case modeConfirm:
		return modalStyle.Render(truncate(m.confirmPrompt, m.width))
	case modePrompt:
		line := m.promptTitle + " " + m.textInput.View()
		return modalStyle.Render(truncate(line, m.width))
	}
	// A failed action takes the footer until the next keypress. Errors used to
	// render only when the preview happened to be empty — which in normal use
	// it never is — so a failed kill, rename, or new-window looked exactly like
	// a successful one.
	if m.err != nil {
		return errorStyle.Render(truncate("error: "+m.err.Error()+"  (any key dismisses)", m.width))
	}
	if m.info != "" {
		return infoStyle.Render(truncate(m.info+"  (any key dismisses)", m.width))
	}
	if m.filtering || m.filter != "" {
		return m.renderFilterLine()
	}
	keys := m.helpKeys()
	return helpStyle.Render(truncate(keys, m.width))
}

// renderFilterLine shows the active filter, with a trailing cursor while it's
// being typed.
func (m Model) renderFilterLine() string {
	cursor := ""
	if m.filtering {
		cursor = "_"
	}
	line := "/" + m.filter + cursor
	if !m.filtering {
		line += "  (esc clears)"
	}
	return filterStyle.Render(truncate(line, m.width))
}

// helpSection is one titled group of key bindings in the full help overlay.
type helpSection struct {
	title   string
	entries [][2]string // {keys, description}
}

// helpSections is the complete keyboard reference shown by the "?" overlay.
var helpSections = []helpSection{
	{"Global", [][2]string{
		{"Tab / Shift-Tab", "cycle focus between panels"},
		{"1 / 2 / 3 / 4", "jump to Projects / Sessions / Windows / Panes"},
		{"↑ ↓  or  j k", "move the selection"},
		{"PgUp / PgDn", "move the selection a screenful"},
		{"g / G", "jump to the first / last entry"},
		{"/", "filter the focused panel"},
		{"Ctrl-F", "fuzzy search in the focused panel"},
		{"Ctrl-Z", "fuzzy search across all panels"},
		{"f", "find a pane anywhere (full TUI)"},
		{"p / [", "open scrollback pager and search"},
		{"y", "copy selection to clipboard"},
		{"Esc", "clear the filter"},
		{"R", "reload the project and session lists"},
		{"M", "open the context menu for the selection"},
		{"m", "toggle mouse capture"},
		{"?", "toggle this help"},
		{"q / Ctrl-C", "quit"},
	}},
	{"Mouse", [][2]string{
		{"Click", "focus a panel and select a row"},
		{"Double-click", "attach (or start a project)"},
		{"Right-click", "open the context menu (or M)"},
		{"Wheel", "scroll the panel under the pointer"},
	}},
	{"Agent markers", [][2]string{
		{blockedGlyph, "an agent is waiting on an answer"},
		{idleGlyph, "an agent finished and is awaiting input"},
	}},
	{"Projects panel", [][2]string{
		{"Enter", "start or attach the config's session"},
		{"e", "edit the config in $EDITOR"},
		{"x", "stop the session (runs on_project_exit)"},
		{"y", "copy project path to clipboard"},
	}},
	{"Sessions panel", [][2]string{
		{"Enter", "attach, or start a stopped session"},
		{"a", "also list stopped projects"},
		{"x", "kill the session"},
		{"r", "rename the session"},
		{"n", "new window in this session"},
		{"y", "copy session name to clipboard"},
	}},
	{"Windows panel", [][2]string{
		{"Enter", "attach, landing on this window"},
		{"x", "kill the window"},
		{"r", "rename the window"},
		{"n", "new window"},
		{"< / >", "reorder window up / down"},
		{"W", "move window to session"},
		{"s / v", "split pane vertically"},
		{"S", "split pane horizontally"},
		{"L", "cycle the window layout"},
		{"y", "copy window name to clipboard"},
	}},
	{"Panes & Pager", [][2]string{
		{"Enter", "attach, landing on this pane"},
		{"x", "kill the pane"},
		{"s / v", "split pane vertically"},
		{"S", "split pane horizontally"},
		{"z", "toggle zoom"},
		{"p / [", "open scrollback pager & search"},
		{"y", "copy pane preview or pager buffer"},
	}},
	{"Confirm / prompt", [][2]string{
		{"y", "confirm"},
		{"n / Esc", "cancel"},
	}},
}

// helpColumn renders a set of sections into one aligned block of lines: a
// styled section header followed by its "key  description" rows, blank line
// between sections.
func helpColumn(sections []helpSection) string {
	keyCol := 0
	for _, s := range sections {
		for _, e := range s.entries {
			if w := lipgloss.Width(e[0]); w > keyCol {
				keyCol = w
			}
		}
	}
	var lines []string
	for i, s := range sections {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, blurredTitle.Render(s.title))
		for _, e := range s.entries {
			lines = append(lines, "  "+keyStyle.Render(padRight(e[0], keyCol))+"  "+e[1])
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// helpLines returns the body of the help overlay as individual lines (so it can
// be scrolled). It lays the sections out in two side-by-side columns when the
// terminal is wide enough, and falls back to a single column otherwise. Lines
// are clipped to the available width so a narrow terminal doesn't cut the box's
// right border off mid-word.
func (m Model) helpLines() []string {
	avail := m.width - helpChrome
	half := (len(helpSections) + 1) / 2
	two := lipgloss.JoinHorizontal(lipgloss.Top,
		helpColumn(helpSections[:half]), "    ", helpColumn(helpSections[half:]))
	block := two
	if lipgloss.Width(two) > avail {
		block = helpColumn(helpSections)
	}
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		lines[i] = truncate(l, avail)
	}
	return lines
}

// helpChrome is the horizontal space the overlay's border + padding consume.
const helpChrome = 4 // border (2) + padding (2)

// helpVisible is how many body lines fit between the title and footer.
func (m Model) helpVisible() int {
	// border (2) + title (1) + footer (1).
	v := m.height - 4
	if v < 1 {
		v = 1
	}
	return v
}

func (m Model) helpMaxScroll() int {
	maxScroll := len(m.helpLines()) - m.helpVisible()
	if maxScroll < 0 {
		return 0
	}
	return maxScroll
}

// renderHelpOverlay draws a centered cheat sheet of every binding. When the
// content is taller than the terminal it shows a scrollable window with a
// position indicator instead of overflowing off-screen.
func (m Model) renderHelpOverlay() string {
	lines := m.helpLines()
	visible := m.helpVisible()

	scroll := m.helpScroll
	if maxScroll := len(lines) - visible; scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	end := scroll + visible
	if end > len(lines) {
		end = len(lines)
	}

	title := focusedTitle.Render("wyrm — keyboard shortcuts")
	body := lipgloss.JoinVertical(lipgloss.Left, lines[scroll:end]...)

	var footer string
	if len(lines) > visible {
		pct := 100
		if maxScroll := len(lines) - visible; maxScroll > 0 {
			pct = scroll * 100 / maxScroll
		}
		footer = hintStyle.Render(fmt.Sprintf("%d%%  ·  ↑↓/jk scroll  ·  esc close", pct))
	} else {
		footer = hintStyle.Render("esc close")
	}

	box := focusedBorder.Padding(0, 1).Render(lipgloss.JoinVertical(lipgloss.Left, title, body, footer))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// findPaneVisible is how many rows modeFindPane's list can show: border (2)
// + title (1) + query line (1) + footer (1).
func (m Model) findPaneVisible() int {
	v := m.height - 5
	if v < 1 {
		v = 1
	}
	return v
}

// findPaneRowWidth caps a row's rendered width, the same way the help
// overlay leaves margin around a centered box rather than spanning the
// full terminal.
func (m Model) findPaneRowWidth() int {
	w := m.width - 12
	if w > 100 {
		w = 100
	}
	if w < 20 {
		w = 20
	}
	return w
}

// renderFindPaneOverlay draws the whole-server pane search (modeFindPane):
// a centered box with the typed query, a scrollable list of every pane on
// the server that matches it, and Enter/Esc in the footer — the same
// centered-overlay shape as the help screen, sized to the terminal the
// same way.
func (m Model) renderFindPaneOverlay() string {
	list := m.visibleAllPanes()
	rowW := m.findPaneRowWidth()
	visible := m.findPaneVisible()
	start, end := viewport(m.findPaneCur, len(list), visible)

	title := focusedTitle.Render("wyrm — find pane")
	query := filterStyle.Render(truncate("/"+m.findPaneQuery+"_", rowW))

	var lines []string
	if len(list) == 0 {
		lines = append(lines, hintStyle.Render("no matching panes"))
	}
	for i := start; i < end; i++ {
		p := list[i]
		row := []span{
			plain(p.SessionName + " ▸ "),
			{indexMark, fmt.Sprintf("%d:", p.WindowIndex)},
			plain(p.WindowName + " ▸ "),
			{indexMark, fmt.Sprintf("%d", p.PaneIndex)},
			plain("  " + p.Command),
		}
		lines = append(lines, renderRow(row, rowW, selectedRow, i == m.findPaneCur))
	}
	body := lipgloss.JoinVertical(lipgloss.Left, lines...)

	footer := hintStyle.Render(fmt.Sprintf("%d/%d panes  ·  ↑↓ move  ·  enter attach  ·  esc close",
		len(list), len(m.allPanes)))

	box := focusedBorder.Padding(0, 1).Render(lipgloss.JoinVertical(lipgloss.Left, title, query, body, footer))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// renderMoveWindowOverlay draws the session picker modal to move a window.
func (m Model) renderMoveWindowOverlay() string {
	list := m.moveWindowSessions
	rowW := 40
	if m.width-12 < rowW {
		rowW = max(20, m.width-12)
	}
	visible := min(len(list), m.height-6)
	if visible < 1 {
		visible = 1
	}
	start, end := viewport(m.moveWindowCur, len(list), visible)

	title := focusedTitle.Render("wyrm — move window to session")
	var lines []string
	if len(list) == 0 {
		lines = append(lines, hintStyle.Render("no other active sessions"))
	}
	for i := start; i < end; i++ {
		s := list[i]
		row := []span{
			plain(s.Name),
			{hintStyle, fmt.Sprintf(" (%d windows)", s.Windows)},
		}
		lines = append(lines, renderRow(row, rowW, selectedRow, i == m.moveWindowCur))
	}
	body := lipgloss.JoinVertical(lipgloss.Left, lines...)
	footer := hintStyle.Render("↑↓/jk move  ·  enter transfer  ·  esc cancel")

	box := focusedBorder.Padding(0, 1).Render(lipgloss.JoinVertical(lipgloss.Left, title, body, footer))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// helpKeys returns the contextual key hints for the focused panel.
func (m Model) helpKeys() string {
	if keys := m.focus.spec().keys; keys != "" {
		return keys
	}
	return navKeys
}

// viewport returns the [start,end) slice of a list of length n that keeps
// cursor visible within a window of height rows.
func viewport(cursor, n, rows int) (int, int) {
	if n <= rows {
		return 0, n
	}
	start := cursor - rows/2
	if start < 0 {
		start = 0
	}
	if start+rows > n {
		start = n - rows
	}
	return start, start + rows
}

// truncate clips s to a display width of w columns, appending "…" when cut.
// It is ANSI-aware so embedded color/attribute escapes count as zero width and
// are never split mid-sequence.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// padRight pads s with spaces to a display width of w so a reverse-video
// selected row fills the panel width. Width is measured ANSI-aware so colored
// rows pad to the correct visible column.
func padRight(s string, w int) string {
	gap := w - ansi.StringWidth(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

// pagerBodyHeight is how many buffer lines the pager box actually renders,
// mirroring renderPagerOverlay's own boxH/innerH/bodyH chain exactly.
// Scrolling, paging, "G"/"end", and search-match navigation all have to
// agree with this number — they used to independently assume m.height-4,
// one row taller than the box the renderer really drew, so the last line
// was unreachable and jumping to a search match could scroll past what was
// actually shown.
func (m Model) pagerBodyHeight() int {
	boxH := m.height - 2
	if boxH < 6 {
		boxH = 6
	}
	innerH := boxH - borderSize
	bodyH := innerH - titleRows
	if bodyH < 1 {
		bodyH = 1
	}
	return bodyH
}

func (m Model) renderPagerOverlay() string {
	boxW := m.width - 4
	boxH := m.height - 2
	if boxW < 20 {
		boxW = 20
	}
	if boxH < 6 {
		boxH = 6
	}
	innerW := boxW - borderSize
	innerH := boxH - borderSize
	bodyH := m.pagerBodyHeight()

	title := fmt.Sprintf(" Pager: %s ", m.pagerPaneTitle)
	if len(m.pagerLines) > 0 {
		title += fmt.Sprintf("[%d/%d] ", m.pagerScroll+1, len(m.pagerLines))
	}
	if m.pagerQuery != "" {
		matchInfo := "no matches"
		if len(m.pagerMatches) > 0 {
			matchInfo = fmt.Sprintf("match %d/%d", m.pagerMatchIdx+1, len(m.pagerMatches))
		}
		title += fmt.Sprintf("(search: %q · %s) ", m.pagerQuery, matchInfo)
	}

	var b strings.Builder
	b.WriteString(focusedTitle.Render(truncate(title, innerW)))
	b.WriteByte('\n')

	start := m.pagerScroll
	if start < 0 {
		start = 0
	}
	for i := 0; i < bodyH; i++ {
		lineIdx := start + i
		if lineIdx < len(m.pagerLines) {
			line := m.pagerLines[lineIdx]
			if m.pagerQuery != "" {
				line = highlightMatch(line, m.pagerQuery)
			}
			b.WriteString(truncate(line, innerW))
			b.WriteString(ansi.ResetStyle)
		}
		if i < bodyH-1 {
			b.WriteByte('\n')
		}
	}

	frame := focusedBorder.Width(innerW).Height(innerH).MaxHeight(boxH).Render(b.String())

	var footer string
	if m.pagerSearching {
		footer = filterStyle.Render(truncate("/"+m.pagerQuery+"_  (Enter to commit, Esc to cancel)", m.width))
	} else {
		footer = helpStyle.Render(truncate("↑/↓/j/k: scroll · PgUp/PgDn: page · /: search · n/N: next/prev match · y: copy buffer · q/Esc: exit", m.width))
	}

	return lipgloss.JoinVertical(lipgloss.Left, frame, footer)
}

// highlightMatch wraps every case-insensitive occurrence of query in line
// with searchMatchStyle. It works in rune spans rather than byte offsets:
// strings.ToLower can change a character's byte length (the Kelvin sign
// lowercases from 3 bytes to ASCII "k"'s 1), so an index found in a lowered
// copy does not reliably locate that match in the original byte string —
// slicing line with it can panic or split a multi-byte rune. unicode.ToLower
// maps exactly one rune to one rune, so lowered and original rune slices
// always stay the same length and index-aligned, which byte slices don't.
func highlightMatch(line, query string) string {
	if query == "" {
		return line
	}
	runes := []rune(line)
	lowerRunes := []rune(strings.ToLower(line))
	queryRunes := []rune(strings.ToLower(query))
	n, m := len(lowerRunes), len(queryRunes)
	if m == 0 || m > n {
		return line
	}

	var b strings.Builder
	last := 0
	for i := 0; i+m <= n; i++ {
		match := true
		for j := 0; j < m; j++ {
			if lowerRunes[i+j] != queryRunes[j] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		b.WriteString(string(runes[last:i]))
		b.WriteString(searchMatchStyle.Render(string(runes[i : i+m])))
		last = i + m
		i = last - 1 // loop's i++ resumes the scan right after the match
	}
	b.WriteString(string(runes[last:]))
	return b.String()
}
