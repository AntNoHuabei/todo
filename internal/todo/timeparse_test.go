package todo

import (
	"testing"
	"time"
)

func TestParseDueChineseCommonCases(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, loc)
	tests := []struct {
		name string
		text string
		want string
	}{
		{"tomorrow afternoon", "明天下午三点交周报", "2026-06-09 15:00"},
		{"half hour", "半小时后喝水", "2026-06-08 10:30"},
		{"one hour", "一个小时后喝水", "2026-06-08 11:00"},
		{"one and half hours", "一个半小时后喝水", "2026-06-08 11:30"},
		{"ten minutes prefix", "再过十分钟喝水", "2026-06-08 10:10"},
		{"two hours after", "2个小时之后开会", "2026-06-08 12:00"},
		{"next monday", "下周一9点开会", "2026-06-15 09:00"},
		{"month day", "6月9日晚上8点跑步", "2026-06-09 20:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ParseDue(tt.text, now, loc)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatal("nil due")
			}
			if got.Format("2006-01-02 15:04") != tt.want {
				t.Fatalf("want %s got %s", tt.want, got.Format("2006-01-02 15:04"))
			}
		})
	}
}

func TestParseDueRelativeCleansTimeText(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, loc)
	_, cleaned, err := ParseDue("一个半小时后喝水", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != "喝水" {
		t.Fatalf("unexpected cleaned text %q", cleaned)
	}
}

func TestParseDueNoTime(t *testing.T) {
	due, cleaned, err := ParseDue("买牛奶", time.Now(), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if due != nil || cleaned != "买牛奶" {
		t.Fatalf("unexpected due=%v cleaned=%q", due, cleaned)
	}
}
