package parser

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type CheckItem struct {
	Text    string
	Checked bool
}

type ParsedNote struct {
	Body       string
	Tags       []string
	Date       string
	Reminder   *time.Time
	Repeat     string
	CheckItems []CheckItem
}

var (
	datePattern     = regexp.MustCompile(`(?m)^@date[ \t]+([0-9]{4}-[0-9]{2}-[0-9]{2})[ \t]*$`)
	reminderPattern = regexp.MustCompile(`(?m)^@remind[ \t]+([^ \t\r\n]+)[ \t]*$`)
	repeatPattern   = regexp.MustCompile(`(?m)^@repeat[ \t]+([^ \t\r\n]+)[ \t]*$`)
	tagPattern      = regexp.MustCompile(`#[\p{L}\p{N}_.-]+`)
	checkPattern    = regexp.MustCompile(`(?m)^[ \t]*-[ \t]+\[([ xX])\][ \t]+(.*)$`)
	repeatValue     = regexp.MustCompile(`^(daily|weekly|monthly|every:[1-9][0-9]*d)$`)
)

func Parse(body string) ParsedNote {
	parsed := ParsedNote{Body: body}
	parsed.Tags = parseTags(body)

	if match := datePattern.FindStringSubmatch(body); len(match) == 2 {
		if _, err := time.Parse("2006-01-02", match[1]); err == nil {
			parsed.Date = match[1]
		}
	}

	if match := reminderPattern.FindStringSubmatch(body); len(match) == 2 {
		if reminder, err := time.Parse(time.RFC3339, match[1]); err == nil {
			reminder = reminder.UTC()
			parsed.Reminder = &reminder
		}
	}

	if match := repeatPattern.FindStringSubmatch(body); len(match) == 2 && repeatValue.MatchString(match[1]) {
		parsed.Repeat = match[1]
	}

	for _, match := range checkPattern.FindAllStringSubmatch(body, -1) {
		parsed.CheckItems = append(parsed.CheckItems, CheckItem{
			Text:    match[2],
			Checked: match[1] != " ",
		})
	}

	return parsed
}

func parseTags(body string) []string {
	seen := make(map[string]struct{})
	var tags []string
	for _, match := range tagPattern.FindAllStringIndex(body, -1) {
		start, end := match[0], match[1]
		if start > 0 {
			before, _ := utf8.DecodeLastRuneInString(body[:start])
			if unicode.IsLetter(before) || unicode.IsDigit(before) || before == '_' || before == '-' {
				continue
			}
		}
		value := body[start+1 : end]
		if strings.Contains(value, ".") {
			continue
		}
		value = strings.ToLower(value)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		tags = append(tags, value)
	}
	return tags
}

func (p ParsedNote) HasUncheckedItems() bool {
	for _, item := range p.CheckItems {
		if !item.Checked {
			return true
		}
	}
	return false
}
