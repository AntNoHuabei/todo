package todo

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reAfterDuration       = regexp.MustCompile(`(?:(\d+(?:\.\d+)?|[零一二两三四五六七八九十百]+|半)(?:个)?(小时|钟头|分钟|分)|(\d+|[零一二两三四五六七八九十百]+)(?:个)?半(小时|钟头))(?:以?后|之后)`)
	reAfterDurationPrefix = regexp.MustCompile(`再?过(?:(\d+(?:\.\d+)?|[零一二两三四五六七八九十百]+|半)(?:个)?(小时|钟头|分钟|分)|(\d+|[零一二两三四五六七八九十百]+)(?:个)?半(小时|钟头))`)
	reClock               = regexp.MustCompile(`(?:(上午|中午|下午|晚上|早上|凌晨))?\s*([零一二两三四五六七八九十\d]{1,3})(?::|：|点半|点)([零一二两三四五六七八九十\d]{1,3})?`)
	reDate                = regexp.MustCompile(`(\d{4})[-/年](\d{1,2})[-/月](\d{1,2})日?`)
	reMonthDay            = regexp.MustCompile(`(\d{1,2})月(\d{1,2})日?`)
)

func ParseDue(text string, now time.Time, loc *time.Location) (*time.Time, string, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "无" || text == "无时间" {
		return nil, "", nil
	}
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)
	if due, cleaned, ok := parseRelativeDue(text, now); ok {
		return due, cleaned, nil
	}

	day := startOfDay(now, loc)
	cleaned := text
	switch {
	case strings.Contains(text, "后天"):
		day = day.AddDate(0, 0, 2)
		cleaned = strings.ReplaceAll(cleaned, "后天", "")
	case strings.Contains(text, "明天"):
		day = day.AddDate(0, 0, 1)
		cleaned = strings.ReplaceAll(cleaned, "明天", "")
	case strings.Contains(text, "今天"):
		cleaned = strings.ReplaceAll(cleaned, "今天", "")
	case strings.Contains(text, "下周"):
		weekday, ok := parseWeekday(text)
		if ok {
			days := (int(weekday) - int(now.Weekday()) + 7) % 7
			day = startOfDay(now, loc).AddDate(0, 0, days+7)
			cleaned = regexp.MustCompile(`下周[一二三四五六日天]`).ReplaceAllString(cleaned, "")
		}
	case strings.Contains(text, "周") || strings.Contains(text, "星期"):
		weekday, ok := parseWeekday(text)
		if ok {
			days := (int(weekday) - int(now.Weekday()) + 7) % 7
			if days == 0 {
				days = 7
			}
			day = startOfDay(now, loc).AddDate(0, 0, days)
			cleaned = regexp.MustCompile(`(周|星期)[一二三四五六日天]`).ReplaceAllString(cleaned, "")
		}
	default:
		if m := reDate.FindStringSubmatch(text); len(m) > 0 {
			y, _ := strconv.Atoi(m[1])
			mon, _ := strconv.Atoi(m[2])
			d, _ := strconv.Atoi(m[3])
			day = time.Date(y, time.Month(mon), d, 0, 0, 0, 0, loc)
			cleaned = reDate.ReplaceAllString(cleaned, "")
		} else if m := reMonthDay.FindStringSubmatch(text); len(m) > 0 {
			mon, _ := strconv.Atoi(m[1])
			d, _ := strconv.Atoi(m[2])
			day = time.Date(now.Year(), time.Month(mon), d, 0, 0, 0, 0, loc)
			if day.Before(now) {
				day = day.AddDate(1, 0, 0)
			}
			cleaned = reMonthDay.ReplaceAllString(cleaned, "")
		}
	}

	hour, minute, ok := parseClock(text)
	if !ok {
		if cleaned != text {
			due := day.Add(9 * time.Hour)
			return &due, strings.TrimSpace(cleaned), nil
		}
		return nil, text, nil
	}
	cleaned = reClock.ReplaceAllString(cleaned, "")
	due := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
	if due.Before(now) && cleaned == text {
		due = due.AddDate(0, 0, 1)
	}
	return &due, strings.TrimSpace(cleaned), nil
}

func parseRelativeDue(text string, now time.Time) (*time.Time, string, bool) {
	if due, cleaned, ok := parseRelativeDueWithRegexp(text, now, reAfterDuration); ok {
		return due, cleaned, true
	}
	return parseRelativeDueWithRegexp(text, now, reAfterDurationPrefix)
}

func parseRelativeDueWithRegexp(text string, now time.Time, re *regexp.Regexp) (*time.Time, string, bool) {
	m := re.FindStringSubmatch(text)
	if len(m) == 0 {
		return nil, "", false
	}
	minutes, ok := parseDurationMinutes(m)
	if !ok {
		return nil, "", false
	}
	due := now.Add(time.Duration(minutes * float64(time.Minute)))
	cleaned := strings.TrimSpace(re.ReplaceAllString(text, ""))
	return &due, cleaned, true
}

func parseDurationMinutes(m []string) (float64, bool) {
	if m[1] != "" {
		n, ok := parseDurationNumber(m[1])
		if !ok {
			return 0, false
		}
		switch m[2] {
		case "小时", "钟头":
			return n * 60, true
		case "分钟", "分":
			return n, true
		default:
			return 0, false
		}
	}
	if m[3] != "" {
		n, ok := parseDurationNumber(m[3])
		if !ok {
			return 0, false
		}
		switch m[4] {
		case "小时", "钟头":
			return (n + 0.5) * 60, true
		default:
			return 0, false
		}
	}
	return 0, false
}

func parseDurationNumber(text string) (float64, bool) {
	if text == "半" {
		return 0.5, true
	}
	if n, err := strconv.ParseFloat(text, 64); err == nil {
		return n, true
	}
	n, ok := parseChineseNumber(text)
	return float64(n), ok
}

func parseChineseNumber(text string) (int, bool) {
	if n, ok := parseSmallNumber(text); ok {
		return n, true
	}
	values := map[rune]int{
		'零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
	}
	runes := []rune(text)
	total := 0
	current := 0
	usedUnit := false
	for _, r := range runes {
		switch r {
		case '百':
			if current == 0 {
				current = 1
			}
			total += current * 100
			current = 0
			usedUnit = true
		case '十':
			if current == 0 {
				current = 1
			}
			total += current * 10
			current = 0
			usedUnit = true
		default:
			n, ok := values[r]
			if !ok {
				return 0, false
			}
			current = n
		}
	}
	if !usedUnit {
		return 0, false
	}
	return total + current, true
}

func parseClock(text string) (int, int, bool) {
	m := reClock.FindStringSubmatch(text)
	if len(m) == 0 {
		return 0, 0, false
	}
	h, ok := parseSmallNumber(m[2])
	if !ok {
		return 0, 0, false
	}
	minute := 0
	if strings.Contains(m[0], "点半") {
		minute = 30
	} else if m[3] != "" {
		var ok bool
		minute, ok = parseSmallNumber(m[3])
		if !ok {
			return 0, 0, false
		}
	}
	switch m[1] {
	case "下午", "晚上":
		if h < 12 {
			h += 12
		}
	case "中午":
		if h < 11 {
			h += 12
		}
	case "凌晨":
		if h == 12 {
			h = 0
		}
	}
	if h > 23 || minute > 59 {
		return 0, 0, false
	}
	return h, minute, true
}

func parseSmallNumber(text string) (int, bool) {
	if n, err := strconv.Atoi(text); err == nil {
		return n, true
	}
	values := map[rune]int{
		'零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
	}
	runes := []rune(text)
	if len(runes) == 1 {
		if runes[0] == '十' {
			return 10, true
		}
		n, ok := values[runes[0]]
		return n, ok
	}
	if len(runes) == 2 && runes[0] == '十' {
		n, ok := values[runes[1]]
		return 10 + n, ok
	}
	if len(runes) == 2 && runes[1] == '十' {
		n, ok := values[runes[0]]
		return n * 10, ok
	}
	if len(runes) == 3 && runes[1] == '十' {
		a, okA := values[runes[0]]
		b, okB := values[runes[2]]
		return a*10 + b, okA && okB
	}
	return 0, false
}

func parseWeekday(text string) (time.Weekday, bool) {
	for _, part := range []string{"周", "星期"} {
		idx := strings.Index(text, part)
		if idx >= 0 {
			r := []rune(text[idx+len(part):])
			if len(r) == 0 {
				return 0, false
			}
			switch r[0] {
			case '一':
				return time.Monday, true
			case '二':
				return time.Tuesday, true
			case '三':
				return time.Wednesday, true
			case '四':
				return time.Thursday, true
			case '五':
				return time.Friday, true
			case '六':
				return time.Saturday, true
			case '日', '天':
				return time.Sunday, true
			}
		}
	}
	return 0, false
}

func startOfDay(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func ParsePriority(text string) Priority {
	switch {
	case strings.Contains(text, "紧急"), strings.Contains(text, "urgent"):
		return PriorityUrgent
	case strings.Contains(text, "高"), strings.Contains(text, "high"):
		return PriorityHigh
	case strings.Contains(text, "低"), strings.Contains(text, "low"):
		return PriorityLow
	default:
		return PriorityNormal
	}
}

func FormatDue(due *time.Time, loc *time.Location) string {
	if due == nil {
		return "无时间"
	}
	return due.In(loc).Format("2006-01-02 15:04")
}

func MustParseDue(text string, now time.Time, loc *time.Location) *time.Time {
	due, _, err := ParseDue(text, now, loc)
	if err != nil {
		panic(fmt.Sprintf("parse due: %v", err))
	}
	return due
}
