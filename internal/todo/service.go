package todo

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Service struct {
	store *Store
	loc   *time.Location
	now   func() time.Time
}

func NewService(store *Store, loc *time.Location) *Service {
	if loc == nil {
		loc = time.Local
	}
	return &Service{store: store, loc: loc, now: time.Now}
}

func (s *Service) SetNow(now func() time.Time) {
	s.now = now
}

func (s *Service) Create(in CreateInput) (Item, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return Item{}, errors.New("title is required")
	}
	if in.Priority == "" {
		in.Priority = PriorityNormal
	}
	if !validPriority(in.Priority) {
		return Item{}, fmt.Errorf("invalid priority %q", in.Priority)
	}
	now := s.now().In(s.loc)
	var item Item
	err := s.store.Update(func(items []Item) ([]Item, error) {
		idTime := now
		for hasID(items, newID(idTime)) {
			idTime = idTime.Add(time.Nanosecond)
		}
		item = Item{
			ID:        newID(idTime),
			Title:     in.Title,
			Note:      strings.TrimSpace(in.Note),
			ChatID:    strings.TrimSpace(in.ChatID),
			Priority:  in.Priority,
			DueAt:     in.DueAt,
			Status:    StatusOpen,
			CreatedAt: now,
			UpdatedAt: now,
		}
		return append(items, item), nil
	})
	return item, err
}

func (s *Service) List(filter Filter) ([]Item, error) {
	items, err := s.store.List()
	if err != nil {
		return nil, err
	}
	now := s.now().In(s.loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc)
	end := start.Add(24 * time.Hour)
	out := make([]Item, 0, len(items))
	for _, item := range items {
		if !filter.IncludeDeleted && item.Status == StatusDeleted {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.Priority != "" && item.Priority != filter.Priority {
			continue
		}
		if filter.Today {
			if item.DueAt == nil || item.DueAt.Before(start) || !item.DueAt.Before(end) {
				continue
			}
		}
		if filter.Overdue {
			if item.DueAt == nil || item.Status != StatusOpen || item.DueAt.After(now) {
				continue
			}
		}
		out = append(out, item)
	}
	sortItems(out)
	return out, nil
}

func (s *Service) Get(id string) (Item, error) {
	items, err := s.store.List()
	if err != nil {
		return Item{}, err
	}
	idx := matchIndex(items, id)
	if idx < 0 {
		return Item{}, fmt.Errorf("todo %q not found", id)
	}
	return items[idx], nil
}

func (s *Service) Update(id string, in UpdateInput) (Item, error) {
	var updated Item
	err := s.store.Update(func(items []Item) ([]Item, error) {
		idx := matchIndex(items, id)
		if idx < 0 {
			return items, fmt.Errorf("todo %q not found", id)
		}
		item := items[idx]
		if in.Title != nil {
			title := strings.TrimSpace(*in.Title)
			if title == "" {
				return items, errors.New("title is required")
			}
			item.Title = title
		}
		if in.Note != nil {
			item.Note = strings.TrimSpace(*in.Note)
		}
		if in.ChatID != nil {
			item.ChatID = strings.TrimSpace(*in.ChatID)
		}
		if in.Priority != nil {
			if !validPriority(*in.Priority) {
				return items, fmt.Errorf("invalid priority %q", *in.Priority)
			}
			item.Priority = *in.Priority
		}
		if in.DueAt != nil {
			item.DueAt = *in.DueAt
			item.Reminded30m = false
			item.RemindedDue = false
		}
		if in.Status != nil {
			item.Status = *in.Status
			if *in.Status == StatusDone {
				now := s.now().In(s.loc)
				item.DoneAt = &now
			} else if *in.Status == StatusOpen {
				item.DoneAt = nil
			}
		}
		if in.Reminded30m != nil {
			item.Reminded30m = *in.Reminded30m
		}
		if in.RemindedDue != nil {
			item.RemindedDue = *in.RemindedDue
		}
		item.UpdatedAt = s.now().In(s.loc)
		items[idx] = item
		updated = item
		return items, nil
	})
	return updated, err
}

func (s *Service) Complete(id string) (Item, error) {
	status := StatusDone
	return s.Update(id, UpdateInput{Status: &status})
}

func (s *Service) Reopen(id string) (Item, error) {
	status := StatusOpen
	return s.Update(id, UpdateInput{Status: &status})
}

func (s *Service) Delete(id string) (Item, error) {
	status := StatusDeleted
	return s.Update(id, UpdateInput{Status: &status})
}

func (s *Service) Snooze(id string, dur time.Duration) (Item, error) {
	item, err := s.Get(id)
	if err != nil {
		return Item{}, err
	}
	base := s.now().In(s.loc)
	if item.DueAt != nil && item.DueAt.After(base) {
		base = item.DueAt.In(s.loc)
	}
	due := base.Add(dur)
	return s.UpdateDue(id, &due)
}

func (s *Service) UpdateDue(id string, due *time.Time) (Item, error) {
	return s.Update(id, UpdateInput{DueAt: &due})
}

func (s *Service) Summary() (string, error) {
	items, err := s.List(Filter{Status: StatusOpen})
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "当前没有未完成待办。", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "未完成待办 %d 条：\n", len(items))
	for i, item := range items {
		if i >= 10 {
			fmt.Fprintf(&b, "...还有 %d 条", len(items)-i)
			break
		}
		fmt.Fprintf(&b, "%s. [%s] %s", shortID(item.ID), item.Priority, item.Title)
		if item.DueAt != nil {
			fmt.Fprintf(&b, " @ %s", item.DueAt.In(s.loc).Format("2006-01-02 15:04"))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String()), nil
}

func matchIndex(items []Item, id string) int {
	id = strings.TrimSpace(id)
	for i, item := range items {
		if item.ID == id || strings.HasPrefix(item.ID, id) {
			return i
		}
	}
	return -1
}

func hasID(items []Item, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func validPriority(p Priority) bool {
	switch p {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent:
		return true
	default:
		return false
	}
}

func newID(now time.Time) string {
	return fmt.Sprintf("%x", now.UnixNano())
}

func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6]
}

func SortByPriority(items []Item) {
	rank := map[Priority]int{
		PriorityUrgent: 0,
		PriorityHigh:   1,
		PriorityNormal: 2,
		PriorityLow:    3,
	}
	sort.SliceStable(items, func(i, j int) bool {
		return rank[items[i].Priority] < rank[items[j].Priority]
	})
}
