package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"todo-assistant/internal/todo"
)

type mode int

const (
	modeList mode = iota
	modeInput
)

type model struct {
	svc    *todo.Service
	items  []todo.Item
	cursor int
	err    string
	input  string
	mode   mode
}

func Run(svc *todo.Service) error {
	m := model{svc: svc}
	m.reload()
	_, err := tea.NewProgram(m).Run()
	return err
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.mode == modeInput {
			switch msg.String() {
			case "esc":
				m.mode = modeList
				m.input = ""
			case "enter":
				m.err = m.createFromInput()
				m.input = ""
				m.mode = modeList
				m.reload()
			case "backspace":
				if len(m.input) > 0 {
					m.input = m.input[:len(m.input)-1]
				}
			default:
				if len(msg.Runes) > 0 {
					m.input += string(msg.Runes)
				}
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "n":
			m.mode = modeInput
		case "d":
			if len(m.items) > 0 {
				_, err := m.svc.Delete(m.items[m.cursor].ID)
				m.err = errString(err)
				m.reload()
			}
		case " ":
			if len(m.items) > 0 {
				_, err := m.svc.Complete(m.items[m.cursor].ID)
				m.err = errString(err)
				m.reload()
			}
		case "r":
			if len(m.items) > 0 {
				_, err := m.svc.Reopen(m.items[m.cursor].ID)
				m.err = errString(err)
				m.reload()
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Render("Todo Assistant")
	help := "n 新建 | space 完成 | r 重开 | d 删除 | j/k 移动 | q 退出"
	if m.mode == modeInput {
		return fmt.Sprintf("%s\n\n新建待办：%s\n\n提示：可输入“明天下午三点交周报 优先级高”\nEsc 取消，Enter 保存\n", title, m.input)
	}
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")
	if len(m.items) == 0 {
		b.WriteString("没有未完成待办。\n")
	}
	for i, item := range m.items {
		cursor := " "
		if i == m.cursor {
			cursor = ">"
		}
		line := fmt.Sprintf("%s %s [%s] %s", cursor, shortID(item.ID), item.Priority, item.Title)
		if item.DueAt != nil {
			line += " @ " + item.DueAt.Format("2006-01-02 15:04")
		}
		if item.Status == todo.StatusDone {
			line += " (done)"
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if m.err != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(m.err))
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString(help)
	b.WriteByte('\n')
	return b.String()
}

func (m *model) reload() {
	items, err := m.svc.List(todo.Filter{})
	if err != nil {
		m.err = err.Error()
		return
	}
	m.items = items
	if m.cursor >= len(m.items) && len(m.items) > 0 {
		m.cursor = len(m.items) - 1
	}
}

func (m *model) createFromInput() string {
	text := strings.TrimSpace(m.input)
	if text == "" {
		return ""
	}
	due, cleaned, err := todo.ParseDue(text, time.Now(), time.Local)
	if err != nil {
		return err.Error()
	}
	title := cleaned
	if title == "" {
		title = text
	}
	title = strings.TrimSpace(strings.Trim(title, "，,。 "))
	priority := todo.ParsePriority(text)
	_, err = m.svc.Create(todo.CreateInput{Title: title, DueAt: due, Priority: priority})
	return errString(err)
}

func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6]
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
