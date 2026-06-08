package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"todo-assistant/internal/todo"
)

type Sender interface {
	SendText(ctx context.Context, chatID, content string) error
}

type MarkdownSender interface {
	SendMarkdown(ctx context.Context, chatID, content string) error
}

type Options struct {
	HomeChatID string
	Interval   time.Duration
	Now        func() time.Time
}

type Scheduler struct {
	svc        *todo.Service
	sender     Sender
	homeChatID string
	interval   time.Duration
	now        func() time.Time
	mu         sync.RWMutex
}

func New(svc *todo.Service, sender Sender, opts Options) *Scheduler {
	if opts.Interval == 0 {
		opts.Interval = time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Scheduler{svc: svc, sender: sender, homeChatID: opts.HomeChatID, interval: opts.Interval, now: opts.Now}
}

func (s *Scheduler) SetHomeChatID(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.homeChatID = chatID
}

func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.Tick(ctx); err != nil {
		return err
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := s.Tick(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *Scheduler) Tick(ctx context.Context) error {
	s.mu.RLock()
	homeChatID := s.homeChatID
	s.mu.RUnlock()

	items, err := s.svc.List(todo.Filter{Status: todo.StatusOpen})
	if err != nil {
		return err
	}
	now := s.now()
	for _, item := range items {
		if item.DueAt == nil {
			continue
		}
		chatID := item.ChatID
		if chatID == "" {
			chatID = homeChatID
		}
		if chatID == "" {
			continue
		}
		due := *item.DueAt
		if !item.Reminded30m && !now.Before(due.Add(-30*time.Minute)) && now.Before(due) {
			if err := s.sendReminder(ctx, chatID, reminderText(item, "⏰ 提前 30 分钟提醒")); err != nil {
				return err
			}
			v := true
			if _, err := s.svc.Update(item.ID, todo.UpdateInput{Reminded30m: &v}); err != nil {
				return err
			}
		}
		if !item.RemindedDue && !now.Before(due) {
			if err := s.sendReminder(ctx, chatID, reminderText(item, "🔔 到时间了")); err != nil {
				return err
			}
			v := true
			if _, err := s.svc.Update(item.ID, todo.UpdateInput{RemindedDue: &v}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Scheduler) sendReminder(ctx context.Context, chatID, content string) error {
	if markdownSender, ok := s.sender.(MarkdownSender); ok {
		return markdownSender.SendMarkdown(ctx, chatID, content)
	}
	return s.sender.SendText(ctx, chatID, content)
}

func reminderText(item todo.Item, header string) string {
	id := shortID(item.ID)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", header)
	fmt.Fprintf(&b, "> %s\n", escapeLine(item.Title))
	fmt.Fprintf(&b, "- ID：`%s`\n", id)
	fmt.Fprintf(&b, "- 优先级：%s `%s`\n", priorityEmoji(item.Priority), item.Priority)
	fmt.Fprintf(&b, "- 时间：%s\n", todo.FormatDue(item.DueAt, time.Local))
	if item.Note != "" {
		fmt.Fprintf(&b, "- 备注：%s\n", escapeLine(item.Note))
	}
	fmt.Fprintf(&b, "\n回复：`完成 %s` / `延后 30 分钟 %s`", id, id)
	return strings.TrimSpace(b.String())
}

func priorityEmoji(priority todo.Priority) string {
	switch priority {
	case todo.PriorityUrgent:
		return "🔥"
	case todo.PriorityHigh:
		return "⚠️"
	case todo.PriorityLow:
		return "🌱"
	default:
		return "📌"
	}
}

func escapeLine(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
}

func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6]
}
