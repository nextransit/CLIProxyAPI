package tui

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// keysTabModel displays and manages API keys.
type keysTabModel struct {
	client   *Client
	viewport viewport.Model
	keys     []string
	gemini   []map[string]any
	claude   []map[string]any
	codex    []map[string]any
	vertex   []map[string]any
	openai   []map[string]any
	err      error
	width    int
	height   int
	ready    bool
	cursor   int
	confirm  int // -1 = no deletion pending
	status   string

	// Editing / Adding
	editing   bool
	adding    bool
	editIdx   int
	editInput textinput.Model

	// OpenAI Compat editing
	editingCompat  bool
	compatIdx      int             // index of the provider being edited
	keyEditingIdx  int             // index of the key being edited (-1 = new key)
	keyEditInput   textinput.Model // for API key
	keyWeightInput textinput.Model // for weight
	keyProxyInput  textinput.Model // for proxy URL
}

type keysDataMsg struct {
	apiKeys []string
	gemini  []map[string]any
	claude  []map[string]any
	codex   []map[string]any
	vertex  []map[string]any
	openai  []map[string]any
	err     error
}

type keyActionMsg struct {
	action string
	err    error
}

func newKeysTabModel(client *Client) keysTabModel {
	ti := textinput.New()
	ti.CharLimit = 512
	ti.Prompt = "  Key: "
	return keysTabModel{
		client:    client,
		confirm:   -1,
		editInput: ti,
	}
}

func (m keysTabModel) Init() tea.Cmd {
	return m.fetchKeys
}

func (m keysTabModel) fetchKeys() tea.Msg {
	result := keysDataMsg{}
	apiKeys, err := m.client.GetAPIKeys()
	if err != nil {
		result.err = err
		return result
	}
	result.apiKeys = apiKeys
	result.gemini, _ = m.client.GetGeminiKeys()
	result.claude, _ = m.client.GetClaudeKeys()
	result.codex, _ = m.client.GetCodexKeys()
	result.vertex, _ = m.client.GetVertexKeys()
	result.openai, _ = m.client.GetOpenAICompat()
	return result
}

func (m keysTabModel) Update(msg tea.Msg) (keysTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case keysDataMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.keys = msg.apiKeys
			m.gemini = msg.gemini
			m.claude = msg.claude
			m.codex = msg.codex
			m.vertex = msg.vertex
			m.openai = msg.openai
			if m.cursor >= len(m.keys) {
				m.cursor = max(0, len(m.keys)-1)
			}
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case keyActionMsg:
		if msg.err != nil {
			m.status = errorStyle.Render("✗ " + msg.err.Error())
		} else {
			m.status = successStyle.Render("✓ " + msg.action)
		}
		m.confirm = -1
		m.viewport.SetContent(m.renderContent())
		return m, m.fetchKeys

	case tea.KeyMsg:
		// ---- OpenAI Compat editing mode ----
		if m.editingCompat {
			return m.handleCompatEditInput(msg)
		}

		// ---- Editing / Adding mode ----
		if m.editing || m.adding {
			switch msg.String() {
			case "enter":
				value := strings.TrimSpace(m.editInput.Value())
				if value == "" {
					m.editing = false
					m.adding = false
					m.editInput.Blur()
					m.viewport.SetContent(m.renderContent())
					return m, nil
				}
				isAdding := m.adding
				editIdx := m.editIdx
				m.editing = false
				m.adding = false
				m.editInput.Blur()
				if isAdding {
					return m, func() tea.Msg {
						err := m.client.AddAPIKey(value)
						if err != nil {
							return keyActionMsg{err: err}
						}
						return keyActionMsg{action: T("key_added")}
					}
				}
				return m, func() tea.Msg {
					err := m.client.EditAPIKey(editIdx, value)
					if err != nil {
						return keyActionMsg{err: err}
					}
					return keyActionMsg{action: T("key_updated")}
				}
			case "esc":
				m.editing = false
				m.adding = false
				m.editInput.Blur()
				m.viewport.SetContent(m.renderContent())
				return m, nil
			default:
				var cmd tea.Cmd
				m.editInput, cmd = m.editInput.Update(msg)
				m.viewport.SetContent(m.renderContent())
				return m, cmd
			}
		}

		// ---- Delete confirmation ----
		if m.confirm >= 0 {
			switch msg.String() {
			case "y", "Y":
				idx := m.confirm
				m.confirm = -1
				return m, func() tea.Msg {
					err := m.client.DeleteAPIKey(idx)
					if err != nil {
						return keyActionMsg{err: err}
					}
					return keyActionMsg{action: T("key_deleted")}
				}
			case "n", "N", "esc":
				m.confirm = -1
				m.viewport.SetContent(m.renderContent())
				return m, nil
			}
		}

		// ---- Normal mode ----
		switch msg.String() {
		case "r":
			m.status = ""
			return m, m.fetchKeys
		case "a", "A":
			m.adding = true
			m.editInput.SetValue("")
			m.editInput.Focus()
			m.viewport.SetContent(m.renderContent())
			return m, textinput.Blink
		case "e", "E":
			if m.cursor < len(m.keys) {
				m.editing = true
				m.editIdx = m.cursor
				m.editInput.SetValue(m.keys[m.cursor])
				m.editInput.Focus()
				m.viewport.SetContent(m.renderContent())
				return m, textinput.Blink
			}
		case "d", "D":
			if m.cursor < len(m.keys) {
				m.confirm = m.cursor
				m.viewport.SetContent(m.renderContent())
			}
		case "c", "C":
			if m.cursor < len(m.keys) {
				if err := clipboard.WriteAll(m.keys[m.cursor]); err == nil {
					m.status = successStyle.Render(T("copied"))
				} else {
					m.status = errorStyle.Render(T("copy_failed"))
				}
				m.viewport.SetContent(m.renderContent())
			}
		case "j", "down":
			if len(m.keys) > 0 {
				m.cursor = (m.cursor + 1) % len(m.keys)
				m.viewport.SetContent(m.renderContent())
			}
		case "k", "up":
			if len(m.keys) > 0 {
				m.cursor = (m.cursor - 1 + len(m.keys)) % len(m.keys)
				m.viewport.SetContent(m.renderContent())
			}
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// handleCompatEditInput handles keyboard input when editing OpenAI compat provider keys
func (m keysTabModel) handleCompatEditInput(msg tea.KeyMsg) (keysTabModel, tea.Cmd) {
	// Tab through fields: key -> proxy -> weight -> enter to save
	switch msg.String() {
	case "tab":
		// Move focus between fields
		if m.keyEditInput.Focused() {
			m.keyEditInput.Blur()
			m.keyProxyInput.Focus()
		} else if m.keyProxyInput.Focused() {
			m.keyProxyInput.Blur()
			m.keyWeightInput.Focus()
		} else if m.keyWeightInput.Focused() {
			m.keyWeightInput.Blur()
			m.keyEditInput.Focus()
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case "enter":
		// Save and move to next key or exit
		return m.saveCompatKeyEdit()
	case "n", "N":
		// Add new key to this provider
		m.keyEditingIdx = len(getAPIKeyEntries(m.openai, m.compatIdx))
		m.initCompatKeyInputs("")
		return m, nil
	case "esc":
		m.editingCompat = false
		m.keyEditingIdx = -1
		m.keyEditInput.Blur()
		m.keyProxyInput.Blur()
		m.keyWeightInput.Blur()
		m.viewport.SetContent(m.renderContent())
		return m, nil
	default:
		// Update active input field
		var cmd tea.Cmd
		if m.keyEditInput.Focused() {
			m.keyEditInput, cmd = m.keyEditInput.Update(msg)
		} else if m.keyProxyInput.Focused() {
			m.keyProxyInput, cmd = m.keyProxyInput.Update(msg)
		} else if m.keyWeightInput.Focused() {
			m.keyWeightInput, cmd = m.keyWeightInput.Update(msg)
		}
		m.viewport.SetContent(m.renderContent())
		return m, cmd
	}
}

func (m keysTabModel) initCompatKeyInputs(apiKey string) {
	apiKeyInput := textinput.New()
	apiKeyInput.CharLimit = 256
	apiKeyInput.Prompt = "    API Key: "
	apiKeyInput.Width = m.width - 20
	apiKeyInput.SetValue(apiKey)
	apiKeyInput.Focus()

	proxyInput := textinput.New()
	proxyInput.CharLimit = 256
	proxyInput.Prompt = "    Proxy:  "
	proxyInput.Width = m.width - 20

	weightInput := textinput.New()
	weightInput.CharLimit = 10
	weightInput.Prompt = "    Weight: "
	weightInput.Width = 10

	m.keyEditInput = apiKeyInput
	m.keyProxyInput = proxyInput
	m.keyWeightInput = weightInput
}

func (m keysTabModel) saveCompatKeyEdit() (keysTabModel, tea.Cmd) {
	providerName := getString(m.openai[m.compatIdx], "name")
	apiKey := strings.TrimSpace(m.keyEditInput.Value())
	proxyURL := strings.TrimSpace(m.keyProxyInput.Value())
	weight := 1

	if weightStr := strings.TrimSpace(m.keyWeightInput.Value()); weightStr != "" {
		var w int
		if _, err := fmt.Sscanf(weightStr, "%d", &w); err == nil && w >= 0 {
			weight = w
		}
	}

	// If no API key entered, just exit
	if apiKey == "" && m.keyEditingIdx >= len(getAPIKeyEntries(m.openai, m.compatIdx)) {
		m.editingCompat = false
		m.keyEditingIdx = -1
		m.viewport.SetContent(m.renderContent())
		return m, nil
	}

	keyIdx := m.keyEditingIdx
	m.keyEditInput.Blur()
	m.keyProxyInput.Blur()
	m.keyWeightInput.Blur()

	return m, func() tea.Msg {
		err := m.client.PatchOpenAICompatKey(providerName, keyIdx, apiKey, proxyURL, weight)
		if err != nil {
			return keyActionMsg{err: err}
		}
		return keyActionMsg{action: T("key_updated")}
	}
}

func getAPIKeyEntries(openai []map[string]any, idx int) []any {
	if idx < 0 || idx >= len(openai) {
		return nil
	}
	if entries, ok := openai[idx]["api-key-entries"].([]any); ok {
		return entries
	}
	return nil
}

func (m *keysTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	m.editInput.Width = w - 16
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.renderContent())
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m keysTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m keysTabModel) renderContent() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("keys_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("keys_help")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", m.width))
	sb.WriteString("\n")

	if m.err != nil {
		sb.WriteString(errorStyle.Render(T("error_prefix") + m.err.Error()))
		sb.WriteString("\n")
		return sb.String()
	}

	// ━━━ Access API Keys (interactive) ━━━
	sb.WriteString(tableHeaderStyle.Render(fmt.Sprintf("  %s (%d)", T("access_keys"), len(m.keys))))
	sb.WriteString("\n")

	if len(m.keys) == 0 {
		sb.WriteString(subtitleStyle.Render(T("no_keys")))
		sb.WriteString("\n")
	}

	for i, key := range m.keys {
		cursor := "  "
		rowStyle := lipgloss.NewStyle()
		if i == m.cursor {
			cursor = "▸ "
			rowStyle = lipgloss.NewStyle().Bold(true)
		}

		row := fmt.Sprintf("%s%d. %s", cursor, i+1, maskKey(key))
		sb.WriteString(rowStyle.Render(row))
		sb.WriteString("\n")

		// Delete confirmation
		if m.confirm == i {
			sb.WriteString(warningStyle.Render(fmt.Sprintf("    "+T("confirm_delete_key"), maskKey(key))))
			sb.WriteString("\n")
		}

		// Edit input
		if m.editing && m.editIdx == i {
			sb.WriteString(m.editInput.View())
			sb.WriteString("\n")
			sb.WriteString(helpStyle.Render(T("enter_save_esc")))
			sb.WriteString("\n")
		}
	}

	// Add input
	if m.adding {
		sb.WriteString("\n")
		sb.WriteString(m.editInput.View())
		sb.WriteString("\n")
		sb.WriteString(helpStyle.Render(T("enter_add")))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")

	// ━━━ Provider Keys (read-only display) ━━━
	renderProviderKeys(&sb, "Gemini API Keys", m.gemini)
	renderProviderKeys(&sb, "Claude API Keys", m.claude)
	renderProviderKeys(&sb, "Codex API Keys", m.codex)
	renderProviderKeys(&sb, "Vertex API Keys", m.vertex)

	// ━━━ OpenAI Compatibility (editable) ━━━
	if len(m.openai) > 0 {
		renderSection(&sb, "OpenAI Compatibility", len(m.openai))
		for i, entry := range m.openai {
			name := getString(entry, "name")
			baseURL := getString(entry, "base-url")
			prefix := getString(entry, "prefix")
			disabled := getBool(entry, "disabled")

			info := name
			if prefix != "" {
				info += " (prefix: " + prefix + ")"
			}
			if disabled {
				info += " [DISABLED]"
			}
			if baseURL != "" && len(baseURL) > 50 {
				baseURL = baseURL[:47] + "..."
			}
			if baseURL != "" {
				info += " → " + baseURL
			}
			sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, info))

			// Show API key entries for this provider
			entries := getAPIKeyEntries(m.openai, i)
			if entries != nil && len(entries) > 0 {
				for j, e := range entries {
					entryMap, ok := e.(map[string]any)
					if !ok {
						continue
					}
					apiKey := getString(entryMap, "api-key")
					proxyURL := getString(entryMap, "proxy-url")
					weight := 1
					if w, ok := entryMap["weight"].(float64); ok {
						weight = int(w)
					}

					keyDisplay := maskKey(apiKey)
					if len(keyDisplay) > 60 {
						keyDisplay = keyDisplay[:57] + "..."
					}
					weightStr := ""
					if weight != 1 {
						weightStr = fmt.Sprintf(" [w:%d]", weight)
					}
					sb.WriteString(fmt.Sprintf("     %d. %s%s\n", j+1, keyDisplay, weightStr))
					if proxyURL != "" {
						proxyDisplay := proxyURL
						if len(proxyDisplay) > 40 {
							proxyDisplay = proxyDisplay[:37] + "..."
						}
						sb.WriteString(fmt.Sprintf("        proxy: %s\n", proxyDisplay))
					}

					// Show key edit fields
					if m.editingCompat && m.compatIdx == i && m.keyEditingIdx == j {
						sb.WriteString(m.keyEditInput.View())
						sb.WriteString("\n")
						sb.WriteString(m.keyProxyInput.View())
						sb.WriteString("\n")
						sb.WriteString(m.keyWeightInput.View())
						sb.WriteString("\n")
						sb.WriteString(helpStyle.Render("  Tab: next field | Enter: save | Esc: cancel"))
						sb.WriteString("\n")
					}
				}
			}

			// Add new key fields
			if m.editingCompat && m.compatIdx == i && m.keyEditingIdx == len(entries) {
				sb.WriteString(helpStyle.Render("  + New key:"))
				sb.WriteString("\n")
				sb.WriteString(m.keyEditInput.View())
				sb.WriteString("\n")
				sb.WriteString(m.keyProxyInput.View())
				sb.WriteString("\n")
				sb.WriteString(m.keyWeightInput.View())
				sb.WriteString("\n")
				sb.WriteString(helpStyle.Render("  Tab: next field | Enter: save | Esc: cancel | N: next key"))
				sb.WriteString("\n")
			}

			// Show add new key option
			if !m.editingCompat || m.compatIdx != i {
				sb.WriteString(fmt.Sprintf("     [%s]\n", helpStyle.Render("[e] edit keys")))
			}
		}
		sb.WriteString("\n")
	}

	if m.status != "" {
		sb.WriteString(m.status)
		sb.WriteString("\n")
	}

	return sb.String()
}

func renderSection(sb *strings.Builder, title string, count int) {
	header := fmt.Sprintf("%s (%d)", title, count)
	sb.WriteString(tableHeaderStyle.Render("  " + header))
	sb.WriteString("\n")
}

func renderProviderKeys(sb *strings.Builder, title string, keys []map[string]any) {
	if len(keys) == 0 {
		return
	}
	renderSection(sb, title, len(keys))
	for i, key := range keys {
		apiKey := getString(key, "api-key")
		prefix := getString(key, "prefix")
		baseURL := getString(key, "base-url")
		info := maskKey(apiKey)
		if prefix != "" {
			info += " (prefix: " + prefix + ")"
		}
		if baseURL != "" {
			info += " → " + baseURL
		}
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, info))
	}
	sb.WriteString("\n")
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}
