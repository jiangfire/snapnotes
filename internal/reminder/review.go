package reminder

import (
	"sort"
	"time"
)

type ReviewCandidate struct {
	NoteID         string
	NextReviewAt   time.Time
	ReviewStage    int
	LastReviewedAt time.Time
}

var reviewIntervals = []time.Duration{
	24 * time.Hour,
	2 * 24 * time.Hour,
	4 * 24 * time.Hour,
	7 * 24 * time.Hour,
	15 * 24 * time.Hour,
	30 * 24 * time.Hour,
	60 * 24 * time.Hour,
}

func SelectReviews(candidates []ReviewCandidate, now time.Time, limit int) []ReviewCandidate {
	if limit <= 0 {
		return []ReviewCandidate{}
	}
	selected := append([]ReviewCandidate(nil), candidates...)
	sort.SliceStable(selected, func(i, j int) bool {
		iDue := !selected[i].NextReviewAt.After(now)
		jDue := !selected[j].NextReviewAt.After(now)
		if iDue != jDue {
			return iDue
		}
		if selected[i].NextReviewAt.Equal(selected[j].NextReviewAt) {
			return selected[i].NoteID < selected[j].NoteID
		}
		return selected[i].NextReviewAt.Before(selected[j].NextReviewAt)
	})
	if len(selected) > limit {
		selected = selected[:limit]
	}
	return selected
}

func AcknowledgeReview(candidate ReviewCandidate, now time.Time) ReviewCandidate {
	stage := candidate.ReviewStage
	if stage < 0 {
		stage = 0
	}
	intervalIndex := 0
	if stage >= len(reviewIntervals) {
		stage = len(reviewIntervals) - 1
		intervalIndex = stage
	} else {
		stage++
		intervalIndex = stage - 1
	}
	candidate.ReviewStage = stage
	candidate.LastReviewedAt = now
	candidate.NextReviewAt = now.Add(reviewIntervals[intervalIndex])
	return candidate
}
