package scheduler

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"todo-assistant/internal/todo"
)

type fakeSender struct {
	chatIDs []string
	msgs    []string
}

func (f *fakeSender) SendText(ctx context.Context, chatID, content string) error {
	f.chatIDs = append(f.chatIDs, chatID)
	f.msgs = append(f.msgs, content)
	return nil
}

type fakeMarkdownSender struct {
	fakeSender
	markdownChatIDs []string
	markdowns       []string
}

func (f *fakeMarkdownSender) SendMarkdown(ctx context.Context, chatID, content string) error {
	f.markdownChatIDs = append(f.markdownChatIDs, chatID)
	f.markdowns = append(f.markdowns, content)
	return nil
}

func TestSchedulerReminderWindowsAndDedup(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	now := time.Date(2026, 6, 8, 9, 30, 0, 0, loc)
	svc.SetNow(func() time.Time { return now })
	due := time.Date(2026, 6, 8, 10, 0, 0, 0, loc)
	if _, err := svc.Create(todo.CreateInput{Title: "开会", Priority: todo.PriorityNormal, DueAt: &due}); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	s := New(svc, sender, Options{HomeChatID: "chat", Now: func() time.Time { return now }})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 1 {
		t.Fatalf("want 30m reminder, got %d", len(sender.msgs))
	}
	if sender.chatIDs[0] != "chat" {
		t.Fatalf("want fallback chat, got %q", sender.chatIDs[0])
	}
	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 1 {
		t.Fatalf("expected dedup, got %d", len(sender.msgs))
	}
	now = due
	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 2 {
		t.Fatalf("want due reminder, got %d", len(sender.msgs))
	}
}

func TestSchedulerReminderUsesItemChatIDAndMarkdown(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	now := time.Date(2026, 6, 8, 9, 30, 0, 0, loc)
	svc.SetNow(func() time.Time { return now })
	due := time.Date(2026, 6, 8, 10, 0, 0, 0, loc)
	if _, err := svc.Create(todo.CreateInput{Title: "standup", ChatID: "item-chat", Priority: todo.PriorityHigh, DueAt: &due}); err != nil {
		t.Fatal(err)
	}
	sender := &fakeMarkdownSender{}
	s := New(svc, sender, Options{HomeChatID: "home-chat", Now: func() time.Time { return now }})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.markdowns) != 1 {
		t.Fatalf("want markdown reminder, got %d", len(sender.markdowns))
	}
	if sender.markdownChatIDs[0] != "item-chat" {
		t.Fatalf("want item chat, got %q", sender.markdownChatIDs[0])
	}
	if !strings.Contains(sender.markdowns[0], "> standup") {
		t.Fatalf("reminder is not markdown-like: %q", sender.markdowns[0])
	}
	if len(sender.msgs) != 0 {
		t.Fatalf("text sender should not be used when markdown sender is available: %#v", sender.msgs)
	}
}
