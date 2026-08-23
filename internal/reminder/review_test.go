package reminder

import (
	"testing"
	"time"
)

func TestSelectReviewsPrioritizesDueItemsAndLimitsFive(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	candidates := []ReviewCandidate{
		{NoteID: "future-1", NextReviewAt: now.Add(3 * time.Hour), ReviewStage: 1},
		{NoteID: "due-1", NextReviewAt: now.Add(-1 * time.Hour), ReviewStage: 1},
		{NoteID: "future-2", NextReviewAt: now.Add(2 * time.Hour), ReviewStage: 1},
		{NoteID: "overdue-1", NextReviewAt: now.Add(-48 * time.Hour), ReviewStage: 2},
		{NoteID: "due-2", NextReviewAt: now, ReviewStage: 1},
		{NoteID: "future-3", NextReviewAt: now.Add(1 * time.Hour), ReviewStage: 1},
		{NoteID: "future-4", NextReviewAt: now.Add(4 * time.Hour), ReviewStage: 1},
	}

	selected := SelectReviews(candidates, now, 5)

	if len(selected) != 5 {
		t.Fatalf("selected count = %d, want 5", len(selected))
	}
	wantIDs := []string{"overdue-1", "due-1", "due-2", "future-3", "future-2"}
	for i, wantID := range wantIDs {
		if selected[i].NoteID != wantID {
			t.Fatalf("selected[%d] = %q, want %q", i, selected[i].NoteID, wantID)
		}
	}
}

func TestSelectReviewsReturnsAllWhenFewerThanLimit(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	candidates := []ReviewCandidate{
		{NoteID: "one", NextReviewAt: now.Add(time.Hour)},
		{NoteID: "two", NextReviewAt: now.Add(2 * time.Hour)},
	}

	selected := SelectReviews(candidates, now, 5)

	if len(selected) != 2 {
		t.Fatalf("selected count = %d, want 2", len(selected))
	}
}

func TestAcknowledgeReviewAdvancesEbbinghausStage(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	candidate := ReviewCandidate{NoteID: "note-1", ReviewStage: 0, NextReviewAt: now}

	updated := AcknowledgeReview(candidate, now)

	if updated.ReviewStage != 1 {
		t.Fatalf("review stage = %d, want 1", updated.ReviewStage)
	}
	if !updated.LastReviewedAt.Equal(now) {
		t.Fatalf("last reviewed = %s, want %s", updated.LastReviewedAt, now)
	}
	if !updated.NextReviewAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("next review = %s, want %s", updated.NextReviewAt, now.Add(24*time.Hour))
	}
}

func TestAcknowledgeReviewCapsAtLongestConfiguredInterval(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	candidate := ReviewCandidate{NoteID: "note-1", ReviewStage: 99, NextReviewAt: now}

	updated := AcknowledgeReview(candidate, now)

	if updated.ReviewStage != 6 {
		t.Fatalf("review stage = %d, want 6", updated.ReviewStage)
	}
	if !updated.NextReviewAt.Equal(now.Add(60 * 24 * time.Hour)) {
		t.Fatalf("next review = %s, want 60 days later", updated.NextReviewAt)
	}
}
