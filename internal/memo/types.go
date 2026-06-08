package memo

import "time"

type Status string

const (
	StatusActive  Status = "active"
	StatusDeleted Status = "deleted"
)

type Item struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content,omitempty"`
	Links     []string  `json:"links,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	ChatID    string    `json:"chat_id,omitempty"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Filter struct {
	Query          string
	Tags           []string
	Limit          int
	IncludeDeleted bool
}

type CreateInput struct {
	Title   string
	Content string
	Links   []string
	Tags    []string
	ChatID  string
}

type UpdateInput struct {
	Title   *string
	Content *string
	Links   *[]string
	Tags    *[]string
	ChatID  *string
	Status  *Status
}
