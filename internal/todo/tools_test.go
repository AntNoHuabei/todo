package todo

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestToolExecutorLifecycle(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, loc)
	svc.SetNow(func() time.Time { return time.Date(2026, 6, 8, 10, 0, 0, 0, loc) })
	exec := NewToolExecutor(svc, loc)

	res, err := exec.Execute(ToolCall{Name: "todo.create", Args: map[string]interface{}{
		"title": "交周报", "due_at": "明天下午三点", "priority": "high",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Item == nil || res.Item.Priority != PriorityHigh || res.Item.DueAt == nil {
		t.Fatalf("unexpected create result: %#v", res)
	}
	id := res.Item.ID[:6]
	if _, err := exec.Execute(ToolCall{Name: "todo.complete", Args: map[string]interface{}{"id": id}}); err != nil {
		t.Fatal(err)
	}
	items, err := svc.List(Filter{Status: StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != StatusDone {
		t.Fatalf("unexpected done list: %#v", items)
	}
}

func TestToolExecutorCreateUsesChatIDFromContext(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, loc)
	exec := NewToolExecutor(svc, loc)

	ctx := WithChatID(context.Background(), "source-chat")
	res, err := exec.ExecuteContext(ctx, ToolCall{Name: "todo.create", Args: map[string]interface{}{
		"title": "write report",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Item == nil || res.Item.ChatID != "source-chat" {
		t.Fatalf("create did not persist context chat_id: %#v", res.Item)
	}
}

func TestToolExecutorCreateParsesDueFromTitle(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, loc)
	svc.SetNow(func() time.Time { return time.Date(2026, 6, 8, 10, 0, 0, 0, loc) })
	exec := NewToolExecutor(svc, loc)

	res, err := exec.Execute(ToolCall{Name: "todo.create", Args: map[string]interface{}{
		"title": "十分钟后喝水",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Item == nil || res.Item.DueAt == nil {
		t.Fatalf("create did not parse due from title: %#v", res.Item)
	}
	if got := res.Item.DueAt.In(loc).Format("2006-01-02 15:04"); got != "2026-06-08 10:10" {
		t.Fatalf("want parsed due 2026-06-08 10:10, got %s", got)
	}
	if res.Item.Title != "喝水" {
		t.Fatalf("want cleaned title, got %q", res.Item.Title)
	}
}

func TestToolExecutorCreateChatIDArgOverridesContext(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, loc)
	exec := NewToolExecutor(svc, loc)

	ctx := WithChatID(context.Background(), "source-chat")
	res, err := exec.ExecuteContext(ctx, ToolCall{Name: "todo.create", Args: map[string]interface{}{
		"title":   "write report",
		"chat_id": "explicit-chat",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Item == nil || res.Item.ChatID != "explicit-chat" {
		t.Fatalf("create did not persist explicit chat_id: %#v", res.Item)
	}
}
