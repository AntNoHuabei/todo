package todo

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type ToolExecutor struct {
	svc *Service
	loc *time.Location
}

func NewToolExecutor(svc *Service, loc *time.Location) *ToolExecutor {
	if loc == nil {
		loc = time.Local
	}
	return &ToolExecutor{svc: svc, loc: loc}
}

type ToolCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type ToolResult struct {
	Message string `json:"message"`
	Item    *Item  `json:"item,omitempty"`
	Items   []Item `json:"items,omitempty"`
}

func (e *ToolExecutor) Execute(call ToolCall) (ToolResult, error) {
	return e.ExecuteContext(context.Background(), call)
}

func (e *ToolExecutor) ExecuteContext(ctx context.Context, call ToolCall) (ToolResult, error) {
	switch call.Name {
	case "todo.create":
		title := strArg(call.Args, "title")
		note := strArg(call.Args, "note")
		priority := Priority(strArg(call.Args, "priority"))
		chatID := strArg(call.Args, "chat_id")
		if chatID == "" {
			chatID = ChatIDFromContext(ctx)
		}
		var due *time.Time
		if dueText := strArg(call.Args, "due_at"); dueText != "" {
			parsed, _, err := ParseDue(dueText, e.now(), e.loc)
			if err != nil {
				return ToolResult{}, err
			}
			due = parsed
		} else if parsed, cleaned, err := ParseDue(title, e.now(), e.loc); err != nil {
			return ToolResult{}, err
		} else if parsed != nil {
			due = parsed
			if cleaned != "" {
				title = cleaned
			}
		}
		item, err := e.svc.Create(CreateInput{Title: title, Note: note, ChatID: chatID, Priority: priority, DueAt: due})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatItemCard("✅ 已创建", item, e.loc), Item: &item}, nil
	case "todo.list":
		items, err := e.svc.List(parseFilter(call.Args))
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatItems(items, e.loc), Items: items}, nil
	case "todo.update":
		id := strArg(call.Args, "id")
		var in UpdateInput
		if v := strArg(call.Args, "title"); v != "" {
			in.Title = &v
		}
		if raw, ok := call.Args["note"]; ok {
			v := fmt.Sprint(raw)
			in.Note = &v
		}
		if raw, ok := call.Args["chat_id"]; ok {
			v := strings.TrimSpace(fmt.Sprint(raw))
			in.ChatID = &v
		}
		if v := strArg(call.Args, "priority"); v != "" {
			p := Priority(v)
			in.Priority = &p
		}
		if raw, ok := call.Args["due_at"]; ok {
			text := strings.TrimSpace(fmt.Sprint(raw))
			var due *time.Time
			if text != "" && text != "null" && text != "无" {
				parsed, _, err := ParseDue(text, e.now(), e.loc)
				if err != nil {
					return ToolResult{}, err
				}
				due = parsed
			}
			in.DueAt = &due
		}
		item, err := e.svc.Update(id, in)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatItemCard("✏️ 已更新", item, e.loc), Item: &item}, nil
	case "todo.complete":
		item, err := e.svc.Complete(strArg(call.Args, "id"))
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatItemCard("✅ 已完成", item, e.loc), Item: &item}, nil
	case "todo.reopen":
		item, err := e.svc.Reopen(strArg(call.Args, "id"))
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatItemCard("🟢 已重新打开", item, e.loc), Item: &item}, nil
	case "todo.delete":
		item, err := e.svc.Delete(strArg(call.Args, "id"))
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatItemCard("🗑️ 已删除", item, e.loc), Item: &item}, nil
	case "todo.snooze":
		id := strArg(call.Args, "id")
		mins := intArg(call.Args, "minutes", 30)
		item, err := e.svc.Snooze(id, time.Duration(mins)*time.Minute)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatItemCard("😴 已延后", item, e.loc), Item: &item}, nil
	case "todo.summary":
		msg, err := e.svc.Summary()
		return ToolResult{Message: msg}, err
	default:
		return ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}
}

func (e *ToolExecutor) now() time.Time {
	if e.svc != nil && e.svc.now != nil {
		return e.svc.now().In(e.loc)
	}
	return time.Now().In(e.loc)
}

func parseFilter(args map[string]interface{}) Filter {
	var f Filter
	switch strArg(args, "status") {
	case "open", "未完成":
		f.Status = StatusOpen
	case "done", "已完成":
		f.Status = StatusDone
	}
	f.Today = boolArg(args, "today")
	f.Overdue = boolArg(args, "overdue")
	if p := strArg(args, "priority"); p != "" {
		f.Priority = Priority(p)
	}
	return f
}

func ParseToolCallJSON(text string) (ToolCall, error) {
	var call ToolCall
	if err := json.Unmarshal([]byte(text), &call); err != nil {
		return call, err
	}
	if call.Args == nil {
		call.Args = map[string]interface{}{}
	}
	return call, nil
}

func formatItems(items []Item, loc *time.Location) string {
	if len(items) == 0 {
		return "✅ 没有匹配的待办。"
	}
	var b strings.Builder
	b.WriteString("| 状态 | 优先级 | 时间 | ID | 标题 |\n")
	b.WriteString("|---|---|---|---|---|\n")
	now := time.Now().In(loc)
	for _, item := range items {
		fmt.Fprintf(
			&b,
			"| %s | %s | %s | `%s` | %s |\n",
			statusEmoji(item.Status),
			priorityEmoji(item.Priority),
			timeCell(item.DueAt, loc, now),
			shortID(item.ID),
			escapeMarkdownTable(item.Title),
		)
	}
	return strings.TrimSpace(b.String())
}

func formatItemLine(item Item, loc *time.Location) string {
	return fmt.Sprintf("%s %s `%s` %s @ %s",
		statusEmoji(item.Status), priorityEmoji(item.Priority), shortID(item.ID), item.Title, FormatDue(item.DueAt, loc))
}

func formatItemCard(header string, item Item, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", header)
	fmt.Fprintf(&b, "> %s\n", escapeMarkdownLine(item.Title))
	fmt.Fprintf(&b, "- ID：`%s`\n", shortID(item.ID))
	fmt.Fprintf(&b, "- 优先级：%s `%s`\n", priorityEmoji(item.Priority), item.Priority)
	fmt.Fprintf(&b, "- 时间：%s\n", timeCell(item.DueAt, loc, time.Now().In(loc)))
	if item.Note != "" {
		fmt.Fprintf(&b, "- 备注：%s\n", escapeMarkdownLine(item.Note))
	}
	return strings.TrimSpace(b.String())
}

func statusEmoji(status Status) string {
	switch status {
	case StatusDone:
		return "✅"
	case StatusDeleted:
		return "🗑️"
	default:
		return "🟢"
	}
}

func priorityEmoji(priority Priority) string {
	switch priority {
	case PriorityUrgent:
		return "🔥 urgent"
	case PriorityHigh:
		return "⚠️ high"
	case PriorityLow:
		return "🌱 low"
	default:
		return "📌 normal"
	}
}

func timeCell(due *time.Time, loc *time.Location, now time.Time) string {
	if due == nil {
		return "-"
	}
	local := due.In(loc)
	icon := "🕒"
	if local.Before(now) {
		icon = "⏰"
	} else if sameDay(local, now.In(loc)) {
		icon = "📅"
	}
	return icon + " " + local.Format("2006-01-02 15:04")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func escapeMarkdownTable(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "|", "\\|")
	return strings.TrimSpace(text)
}

func escapeMarkdownLine(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
}

func strArg(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func intArg(args map[string]interface{}, key string, def int) int {
	v := strArg(args, key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func boolArg(args map[string]interface{}, key string) bool {
	v, ok := args[key]
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "是"
	default:
		return false
	}
}
