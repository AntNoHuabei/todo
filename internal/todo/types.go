package todo

import "time"

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

type Status string

const (
	StatusOpen    Status = "open"
	StatusDone    Status = "done"
	StatusDeleted Status = "deleted"
)

type Item struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Note        string     `json:"note,omitempty"`
	ChatID      string     `json:"chat_id,omitempty"`
	Priority    Priority   `json:"priority"`
	DueAt       *time.Time `json:"due_at,omitempty"`
	Status      Status     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DoneAt      *time.Time `json:"done_at,omitempty"`
	Reminded30m bool       `json:"reminded_30m"`
	RemindedDue bool       `json:"reminded_due"`
}

type Filter struct {
	Status         Status
	Today          bool
	Overdue        bool
	Priority       Priority
	IncludeDeleted bool
}

type CreateInput struct {
	Title    string
	Note     string
	ChatID   string
	Priority Priority
	DueAt    *time.Time
}

type UpdateInput struct {
	Title       *string
	Note        *string
	ChatID      *string
	Priority    *Priority
	DueAt       **time.Time
	Status      *Status
	Reminded30m *bool
	RemindedDue *bool
}
