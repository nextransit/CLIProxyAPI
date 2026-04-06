package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// logsTabModel displays real-time log lines from hook/API source.
type logsTabModel struct {
	client      *Client
	hook        *LogHook
	viewport    viewport.Model
	lines       []string
	maxLines    int
	autoScroll  bool
	width       int
	height      int
	ready       bool
	filter      string // "", "debug", "info", "warn", "error"
	search      string
	searching   bool
	searchInput textinput.Model
	replaceNext bool
	after       int64
	lastErr     error
}

type logsPollMsg struct {
	lines  []string
	latest int64
	err    error
}

type logsTickMsg struct{}
type logLineMsg string
type setLogsSearchMsg struct {
	query string
}

func newLogsTabModel(client *Client, hook *LogHook) logsTabModel {
	searchInput := textinput.New()
	searchInput.CharLimit = 512
	return logsTabModel{
		client:      client,
		hook:        hook,
		maxLines:    5000,
		autoScroll:  true,
		searchInput: searchInput,
	}
}

func (m logsTabModel) Init() tea.Cmd {
	if m.hook != nil {
		return m.waitForLog
	}
	return m.fetchLogs
}

func (m logsTabModel) fetchLogs() tea.Msg {
	lines, latest, err := m.client.GetLogsFiltered(m.after, 200, m.search)
	return logsPollMsg{
		lines:  lines,
		latest: latest,
		err:    err,
	}
}

func (m logsTabModel) waitForNextPoll() tea.Cmd {
	return tea.Tick(2*time.Second, func(_ time.Time) tea.Msg {
		return logsTickMsg{}
	})
}

func (m logsTabModel) waitForLog() tea.Msg {
	if m.hook == nil {
		return nil
	}
	line, ok := <-m.hook.Chan()
	if !ok {
		return nil
	}
	return logLineMsg(line)
}

func (m logsTabModel) Update(msg tea.Msg) (logsTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderLogs())
		return m, nil
	case setLogsSearchMsg:
		m.search = strings.TrimSpace(msg.query)
		m.searching = false
		m.searchInput.Blur()
		m.lastErr = nil
		m.after = 0
		m.replaceNext = true
		if m.hook == nil {
			m.lines = nil
			m.viewport.SetContent(m.renderLogs())
			return m, m.fetchLogs
		}
		m.viewport.SetContent(m.renderLogs())
		return m, nil
	case logsTickMsg:
		if m.hook != nil {
			return m, nil
		}
		return m, m.fetchLogs
	case logsPollMsg:
		if m.hook != nil {
			return m, nil
		}
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			m.after = msg.latest
			if len(msg.lines) > 0 {
				if m.replaceNext {
					m.lines = append([]string{}, msg.lines...)
				} else {
					m.lines = append(m.lines, msg.lines...)
				}
				if len(m.lines) > m.maxLines {
					m.lines = m.lines[len(m.lines)-m.maxLines:]
				}
			}
			m.replaceNext = false
		}
		m.viewport.SetContent(m.renderLogs())
		if m.autoScroll {
			m.viewport.GotoBottom()
		}
		return m, m.waitForNextPoll()
	case logLineMsg:
		m.lines = append(m.lines, string(msg))
		if len(m.lines) > m.maxLines {
			m.lines = m.lines[len(m.lines)-m.maxLines:]
		}
		m.viewport.SetContent(m.renderLogs())
		if m.autoScroll {
			m.viewport.GotoBottom()
		}
		return m, m.waitForLog

	case tea.KeyMsg:
		if m.searching {
			return m.handleSearchInput(msg)
		}
		switch msg.String() {
		case "a":
			m.autoScroll = !m.autoScroll
			if m.autoScroll {
				m.viewport.GotoBottom()
			}
			return m, nil
		case "c":
			m.lines = nil
			m.lastErr = nil
			m.viewport.SetContent(m.renderLogs())
			return m, nil
		case "/", "s":
			return m.startSearch()
		case "x":
			return m, func() tea.Msg { return setLogsSearchMsg{query: ""} }
		case "1":
			m.filter = ""
			m.viewport.SetContent(m.renderLogs())
			return m, nil
		case "2":
			m.filter = "info"
			m.viewport.SetContent(m.renderLogs())
			return m, nil
		case "3":
			m.filter = "warn"
			m.viewport.SetContent(m.renderLogs())
			return m, nil
		case "4":
			m.filter = "error"
			m.viewport.SetContent(m.renderLogs())
			return m, nil
		default:
			wasAtBottom := m.viewport.AtBottom()
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			// If user scrolls up, disable auto-scroll
			if !m.viewport.AtBottom() && wasAtBottom {
				m.autoScroll = false
			}
			// If user scrolls to bottom, re-enable auto-scroll
			if m.viewport.AtBottom() {
				m.autoScroll = true
			}
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *logsTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	m.searchInput.Width = w - 20
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.renderLogs())
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m logsTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m logsTabModel) renderLogs() string {
	var sb strings.Builder

	scrollStatus := successStyle.Render(T("logs_auto_scroll"))
	if !m.autoScroll {
		scrollStatus = warningStyle.Render(T("logs_paused"))
	}
	filterLabel := "ALL"
	if m.filter != "" {
		filterLabel = strings.ToUpper(m.filter) + "+"
	}
	searchLabel := T("not_set")
	if m.search != "" {
		searchLabel = m.search
	}

	header := fmt.Sprintf(" %s  %s  %s: %s  %s: %s  %s: %d",
		T("logs_title"), scrollStatus, T("logs_filter"), filterLabel, T("logs_search"), searchLabel, T("logs_lines"), len(m.lines))
	sb.WriteString(titleStyle.Render(header))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("logs_help")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", m.width))
	sb.WriteString("\n")

	if m.searching {
		sb.WriteString(m.searchInput.View())
		sb.WriteString("\n")
		sb.WriteString(helpStyle.Render("    " + T("logs_search_submit")))
		sb.WriteString("\n")
	}

	if m.lastErr != nil {
		sb.WriteString(errorStyle.Render("⚠ Error: " + m.lastErr.Error()))
		sb.WriteString("\n")
	}

	if len(m.lines) == 0 {
		sb.WriteString(subtitleStyle.Render(T("logs_waiting")))
		return sb.String()
	}

	for _, line := range m.lines {
		if !m.lineMatchesFilters(line) {
			continue
		}
		styled := m.styleLine(line)
		sb.WriteString(styled)
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m logsTabModel) startSearch() (logsTabModel, tea.Cmd) {
	m.searching = true
	m.searchInput.Focus()
	m.searchInput.Prompt = fmt.Sprintf("  %s: ", T("logs_search"))
	m.searchInput.SetValue(m.search)
	m.viewport.SetContent(m.renderLogs())
	return m, textinput.Blink
}

func (m logsTabModel) handleSearchInput(msg tea.KeyMsg) (logsTabModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		query := strings.TrimSpace(m.searchInput.Value())
		return m, func() tea.Msg { return setLogsSearchMsg{query: query} }
	case "esc":
		m.searching = false
		m.searchInput.Blur()
		m.viewport.SetContent(m.renderLogs())
		return m, nil
	default:
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		m.viewport.SetContent(m.renderLogs())
		return m, cmd
	}
}

func (m logsTabModel) lineMatchesFilters(line string) bool {
	if m.search != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(m.search)) {
		return false
	}
	if m.filter != "" && !m.matchLevel(line) {
		return false
	}
	return true
}

func (m logsTabModel) matchLevel(line string) bool {
	switch m.filter {
	case "error":
		return strings.Contains(line, "[error]") || strings.Contains(line, "[fatal]") || strings.Contains(line, "[panic]")
	case "warn":
		return strings.Contains(line, "[warn") || strings.Contains(line, "[error]") || strings.Contains(line, "[fatal]")
	case "info":
		return !strings.Contains(line, "[debug]")
	default:
		return true
	}
}

func (m logsTabModel) styleLine(line string) string {
	if strings.Contains(line, "[error]") || strings.Contains(line, "[fatal]") {
		return logErrorStyle.Render(line)
	}
	if strings.Contains(line, "[warn") {
		return logWarnStyle.Render(line)
	}
	if strings.Contains(line, "[info") {
		return logInfoStyle.Render(line)
	}
	if strings.Contains(line, "[debug]") {
		return logDebugStyle.Render(line)
	}
	return line
}
