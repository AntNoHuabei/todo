package memo

import (
	"context"
	"encoding/json"
	"fmt"
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

func (e *ToolExecutor) ExecuteContext(ctx context.Context, call ToolCall) (ToolResult, error) {
	switch call.Name {
	case "memo.create":
		chatID := strArg(call.Args, "chat_id")
		if chatID == "" {
			chatID = ChatIDFromContext(ctx)
		}
		item, err := e.svc.Create(CreateInput{
			Title:   strArg(call.Args, "title"),
			Content: strArg(call.Args, "content"),
			Links:   stringSliceArg(call.Args, "links"),
			Tags:    stringSliceArg(call.Args, "tags"),
			ChatID:  chatID,
		})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatMemoCard("📝 已保存备忘录", item, e.loc), Item: &item}, nil
	case "memo.search":
		items, err := e.svc.List(Filter{
			Query: strArg(call.Args, "query"),
			Tags:  stringSliceArg(call.Args, "tags"),
			Limit: intArg(call.Args, "limit", 10),
		})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatMemos(items, e.loc), Items: items}, nil
	case "memo.list":
		items, err := e.svc.List(Filter{Limit: intArg(call.Args, "limit", 10)})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatMemos(items, e.loc), Items: items}, nil
	case "memo.update":
		var in UpdateInput
		if raw, ok := call.Args["title"]; ok {
			v := strings.TrimSpace(fmt.Sprint(raw))
			in.Title = &v
		}
		if raw, ok := call.Args["content"]; ok {
			v := strings.TrimSpace(fmt.Sprint(raw))
			in.Content = &v
		}
		if _, ok := call.Args["links"]; ok {
			v := stringSliceArg(call.Args, "links")
			in.Links = &v
		}
		if _, ok := call.Args["tags"]; ok {
			v := stringSliceArg(call.Args, "tags")
			in.Tags = &v
		}
		item, err := e.svc.Update(strArg(call.Args, "id"), in)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatMemoCard("✏️ 已更新备忘录", item, e.loc), Item: &item}, nil
	case "memo.delete":
		item, err := e.svc.Delete(strArg(call.Args, "id"))
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Message: formatMemoCard("🗑️ 已删除备忘录", item, e.loc), Item: &item}, nil
	default:
		return ToolResult{}, fmt.Errorf("unknown memo tool %q", call.Name)
	}
}

func formatMemos(items []Item, loc *time.Location) string {
	if len(items) == 0 {
		return "没有匹配的备忘录。"
	}
	var b strings.Builder
	b.WriteString("| ID | 标题 | 标签 | 链接 | 更新 |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, item := range items {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n",
			shortID(item.ID),
			escapeMarkdownTable(item.Title),
			escapeMarkdownTable(strings.Join(item.Tags, ", ")),
			escapeMarkdownTable(strings.Join(item.Links, ", ")),
			item.UpdatedAt.In(loc).Format("2006-01-02 15:04"),
		)
	}
	return strings.TrimSpace(b.String())
}

func formatMemoCard(header string, item Item, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", header)
	fmt.Fprintf(&b, "> %s\n", escapeMarkdownLine(item.Title))
	fmt.Fprintf(&b, "- ID：`%s`\n", shortID(item.ID))
	if item.Content != "" {
		fmt.Fprintf(&b, "- 内容：%s\n", escapeMarkdownLine(item.Content))
	}
	if len(item.Links) > 0 {
		fmt.Fprintf(&b, "- 链接：%s\n", escapeMarkdownLine(strings.Join(item.Links, ", ")))
	}
	if len(item.Tags) > 0 {
		fmt.Fprintf(&b, "- 标签：%s\n", escapeMarkdownLine(strings.Join(item.Tags, ", ")))
	}
	fmt.Fprintf(&b, "- 更新：%s", item.UpdatedAt.In(loc).Format("2006-01-02 15:04"))
	return strings.TrimSpace(b.String())
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
	raw := strArg(args, key)
	if raw == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return def
	}
	return n
}

func stringSliceArg(args map[string]interface{}, key string) []string {
	if args == nil {
		return nil
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		var out []string
		if err := json.Unmarshal([]byte(v), &out); err == nil {
			return out
		}
		return strings.Split(v, ",")
	default:
		return []string{fmt.Sprint(v)}
	}
}

func escapeMarkdownTable(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "|", "\\|")
	return strings.TrimSpace(text)
}

func escapeMarkdownLine(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
}
