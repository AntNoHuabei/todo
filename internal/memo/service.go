package memo

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

var reURL = regexp.MustCompile(`https?://[^\s<>"'，。；、)）\]}]+`)

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
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Item{}, errors.New("title is required")
	}
	now := s.now().In(s.loc)
	links := normalizeLinks(append(append([]string{}, in.Links...), ExtractLinks(title+" "+in.Content)...))
	item := Item{
		ID:        newID(now),
		Title:     title,
		Content:   strings.TrimSpace(in.Content),
		Links:     links,
		Tags:      normalizeList(in.Tags),
		ChatID:    strings.TrimSpace(in.ChatID),
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	err := s.store.Update(func(items []Item) ([]Item, error) {
		idTime := now
		for hasID(items, item.ID) {
			idTime = idTime.Add(time.Nanosecond)
			item.ID = newID(idTime)
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
	out := make([]Item, 0, len(items))
	query := normalizeQuery(filter.Query)
	linkOnlyQuery := query == "" && mentionsLink(filter.Query)
	tags := normalizeList(filter.Tags)
	for _, item := range items {
		if !filter.IncludeDeleted && item.Status == StatusDeleted {
			continue
		}
		if linkOnlyQuery && len(item.Links) == 0 {
			continue
		}
		if query != "" && !itemMatchesQuery(item, query) {
			continue
		}
		if len(tags) > 0 && !itemMatchesTags(item, tags) {
			continue
		}
		out = append(out, item)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func (s *Service) Get(id string) (Item, error) {
	items, err := s.store.List()
	if err != nil {
		return Item{}, err
	}
	idx := matchIndex(items, id)
	if idx < 0 {
		return Item{}, fmt.Errorf("memo %q not found", id)
	}
	return items[idx], nil
}

func (s *Service) Update(id string, in UpdateInput) (Item, error) {
	var updated Item
	err := s.store.Update(func(items []Item) ([]Item, error) {
		idx := matchIndex(items, id)
		if idx < 0 {
			return items, fmt.Errorf("memo %q not found", id)
		}
		item := items[idx]
		if in.Title != nil {
			title := strings.TrimSpace(*in.Title)
			if title == "" {
				return items, errors.New("title is required")
			}
			item.Title = title
		}
		if in.Content != nil {
			item.Content = strings.TrimSpace(*in.Content)
		}
		if in.Links != nil {
			item.Links = normalizeLinks(*in.Links)
		}
		if in.Tags != nil {
			item.Tags = normalizeList(*in.Tags)
		}
		if in.ChatID != nil {
			item.ChatID = strings.TrimSpace(*in.ChatID)
		}
		if in.Status != nil {
			if *in.Status != StatusActive && *in.Status != StatusDeleted {
				return items, fmt.Errorf("invalid status %q", *in.Status)
			}
			item.Status = *in.Status
		}
		item.Links = normalizeLinks(append(append([]string{}, item.Links...), ExtractLinks(item.Title+" "+item.Content)...))
		item.UpdatedAt = s.now().In(s.loc)
		items[idx] = item
		updated = item
		return items, nil
	})
	return updated, err
}

func (s *Service) Delete(id string) (Item, error) {
	status := StatusDeleted
	return s.Update(id, UpdateInput{Status: &status})
}

func ExtractLinks(text string) []string {
	return normalizeLinks(reURL.FindAllString(text, -1))
}

func itemMatchesQuery(item Item, query string) bool {
	haystack := compact(strings.Join(append([]string{item.Title, item.Content}, append(item.Links, item.Tags...)...), " "))
	return strings.Contains(haystack, query)
}

func normalizeQuery(text string) string {
	text = compact(text)
	for _, filler := range []string{"的", "备忘录", "备忘", "资料", "链接", "地址"} {
		text = strings.ReplaceAll(text, filler, "")
	}
	return text
}

func mentionsLink(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "链接") ||
		strings.Contains(text, "地址") ||
		strings.Contains(text, "http://") ||
		strings.Contains(text, "https://")
}

func itemMatchesTags(item Item, tags []string) bool {
	itemTags := normalizeList(item.Tags)
	for _, tag := range tags {
		if !slices.Contains(itemTags, tag) {
			return false
		}
	}
	return true
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

func normalizeList(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func normalizeLinks(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if parsed, err := url.Parse(value); err != nil || parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func compact(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), ""))
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
