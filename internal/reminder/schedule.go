package reminder

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusDue       Status = "due"
	StatusOverdue   Status = "overdue"
	StatusDismissed Status = "dismissed"
)

type Schedule struct {
	NextFireAt         time.Time
	Repeat             string
	Timezone           *time.Location
	LastAcknowledgedAt *time.Time
	dismissed          bool
}

func RestoreSchedule(nextFireAt time.Time, repeat string, timezone *time.Location, acknowledgedAt *time.Time, dismissed bool) (Schedule, error) {
	schedule, err := NewSchedule(nextFireAt, repeat, timezone)
	if err != nil {
		return Schedule{}, err
	}
	schedule.LastAcknowledgedAt = acknowledgedAt
	schedule.dismissed = dismissed
	return schedule, nil
}

func (s Schedule) Dismissed() bool {
	return s.dismissed
}

func NewSchedule(fireAt time.Time, repeat string, timezone *time.Location) (Schedule, error) {
	if timezone == nil {
		timezone = time.UTC
	}
	if !validRepeat(repeat) {
		return Schedule{}, fmt.Errorf("unsupported repeat rule %q", repeat)
	}
	return Schedule{NextFireAt: fireAt.UTC(), Repeat: repeat, Timezone: timezone}, nil
}

func (s Schedule) DisplayFireAt() time.Time {
	return s.NextFireAt.In(s.Timezone)
}

func (s Schedule) StatusAt(now time.Time) Status {
	if s.dismissed {
		return StatusDismissed
	}
	if now.Before(s.NextFireAt) {
		return StatusActive
	}
	nowLocal := now.In(s.Timezone)
	fireLocal := s.DisplayFireAt()
	if nowLocal.Year() == fireLocal.Year() && nowLocal.YearDay() == fireLocal.YearDay() {
		return StatusDue
	}
	return StatusOverdue
}

func (s Schedule) Acknowledge(at time.Time) (Schedule, error) {
	acknowledgedAt := at.UTC()
	s.LastAcknowledgedAt = &acknowledgedAt
	if s.Repeat == "" {
		s.dismissed = true
		return s, nil
	}

	next := s.NextFireAt
	for !next.After(at) {
		var err error
		next, err = nextOccurrence(next, s.Repeat)
		if err != nil {
			return Schedule{}, err
		}
	}
	s.NextFireAt = next
	return s, nil
}

func validRepeat(repeat string) bool {
	if repeat == "" || repeat == "daily" || repeat == "weekly" || repeat == "monthly" {
		return true
	}
	if !strings.HasPrefix(repeat, "every:") || !strings.HasSuffix(repeat, "d") {
		return false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(repeat, "every:"), "d")
	n, err := strconv.Atoi(value)
	return err == nil && n > 0
}

func nextOccurrence(current time.Time, repeat string) (time.Time, error) {
	switch repeat {
	case "daily":
		return current.AddDate(0, 0, 1), nil
	case "weekly":
		return current.AddDate(0, 0, 7), nil
	case "monthly":
		return current.AddDate(0, 1, 0), nil
	}
	if strings.HasPrefix(repeat, "every:") && strings.HasSuffix(repeat, "d") {
		value := strings.TrimSuffix(strings.TrimPrefix(repeat, "every:"), "d")
		days, err := strconv.Atoi(value)
		if err != nil || days <= 0 {
			return time.Time{}, errors.New("invalid day interval")
		}
		return current.AddDate(0, 0, days), nil
	}
	return time.Time{}, fmt.Errorf("unsupported repeat rule %q", repeat)
}
