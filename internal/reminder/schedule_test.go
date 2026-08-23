package reminder

import (
	"testing"
	"time"
)

func TestScheduleStoresUTCAndRendersInConfiguredTimezone(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	localFireAt := time.Date(2026, 8, 20, 9, 0, 0, 0, location)

	schedule, err := NewSchedule(localFireAt, "", location)
	if err != nil {
		t.Fatalf("NewSchedule returned error: %v", err)
	}

	if !schedule.NextFireAt.Equal(time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("stored fire time = %s, want UTC 01:00", schedule.NextFireAt)
	}
	if got := schedule.DisplayFireAt(); !got.Equal(localFireAt) {
		t.Fatalf("display fire time = %s, want %s", got, localFireAt)
	}
}

func TestScheduleStatusUsesConfiguredLocalDate(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	schedule, err := NewSchedule(time.Date(2026, 8, 20, 9, 0, 0, 0, location), "", location)
	if err != nil {
		t.Fatalf("NewSchedule returned error: %v", err)
	}

	if got := schedule.StatusAt(time.Date(2026, 8, 20, 8, 59, 0, 0, location)); got != StatusActive {
		t.Fatalf("status before fire = %q, want %q", got, StatusActive)
	}
	if got := schedule.StatusAt(time.Date(2026, 8, 20, 18, 0, 0, 0, location)); got != StatusDue {
		t.Fatalf("status same local date = %q, want %q", got, StatusDue)
	}
	if got := schedule.StatusAt(time.Date(2026, 8, 21, 0, 1, 0, 0, location)); got != StatusOverdue {
		t.Fatalf("status next local date = %q, want %q", got, StatusOverdue)
	}
}

func TestAcknowledgeRecurringReminderSkipsMissedOccurrences(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	schedule, err := NewSchedule(time.Date(2026, 8, 20, 9, 0, 0, 0, location), "every:2d", location)
	if err != nil {
		t.Fatalf("NewSchedule returned error: %v", err)
	}

	acknowledgedAt := time.Date(2026, 8, 25, 12, 0, 0, 0, location)
	updated, err := schedule.Acknowledge(acknowledgedAt)
	if err != nil {
		t.Fatalf("Acknowledge returned error: %v", err)
	}

	wantNext := time.Date(2026, 8, 26, 9, 0, 0, 0, location)
	if !updated.NextFireAt.Equal(wantNext) {
		t.Fatalf("next fire = %s, want %s", updated.NextFireAt, wantNext)
	}
	if updated.LastAcknowledgedAt == nil || !updated.LastAcknowledgedAt.Equal(acknowledgedAt.UTC()) {
		t.Fatalf("last acknowledged = %v, want %s", updated.LastAcknowledgedAt, acknowledgedAt.UTC())
	}
}

func TestAcknowledgeOneShotReminderDismissesIt(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	schedule, err := NewSchedule(time.Date(2026, 8, 20, 9, 0, 0, 0, location), "", location)
	if err != nil {
		t.Fatalf("NewSchedule returned error: %v", err)
	}

	updated, err := schedule.Acknowledge(time.Date(2026, 8, 20, 10, 0, 0, 0, location))
	if err != nil {
		t.Fatalf("Acknowledge returned error: %v", err)
	}
	if updated.StatusAt(time.Date(2026, 8, 20, 10, 1, 0, 0, location)) != StatusDismissed {
		t.Fatalf("status after acknowledgement = %q, want %q", updated.StatusAt(time.Now()), StatusDismissed)
	}
}

func TestNewScheduleRejectsUnknownRepeatRule(t *testing.T) {
	_, err := NewSchedule(time.Now(), "yearly", time.UTC)
	if err == nil {
		t.Fatal("NewSchedule accepted unknown repeat rule")
	}
}
