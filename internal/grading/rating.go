// SPDX-License-Identifier: Apache-2.0

package grading

import (
	"errors"
	"fmt"
)

// FSRS ratings, shared by every problem type.
const (
	RatingAgain = 1
	RatingHard  = 2
	RatingGood  = 3
	RatingEasy  = 4

	// DefaultRating is applied to a right answer when the user picks none.
	DefaultRating = RatingGood
)

// Problem type names as stored on problems.
const (
	ratingTypeMCQ   = "mcq"
	ratingTypeCloze = "cloze"
	ratingTypeOpen  = "open"
)

var (
	// ErrRatingRequired: an open-ended attempt needs a user rating (1-4).
	ErrRatingRequired = errors.New("grading: rating required for open problems")
	// ErrInvalidRating: the rating is outside 1-4, or not allowed for this outcome.
	ErrInvalidRating = errors.New("grading: invalid rating")
	// ErrUnknownProblemType: the problem type is not mcq, cloze or open.
	ErrUnknownProblemType = errors.New("grading: unknown problem type")
)

// openScores maps rating 1-4 to the open-ended score.
var openScores = [4]float64{0, 0.33, 0.67, 1}

// OpenScore derives the score of an open-ended problem from its rating:
// Again 0, Hard 0.33, Good 0.67, Easy 1.
func OpenScore(rating int) (float64, error) {
	if rating < RatingAgain || rating > RatingEasy {
		return 0, fmt.Errorf("%w: %d", ErrInvalidRating, rating)
	}
	return openScores[rating-1], nil
}

// ResolveRating applies the shared rating rules.
//
// For auto-graded types (mcq, cloze) correct reports whether the answer was
// fully right: wrong forces Again regardless of any chosen rating; right uses
// the chosen rating (Hard, Good or Easy) or DefaultRating when chosen is nil.
// Open problems are not auto-graded: correct is ignored and chosen (1-4) is
// required.
func ResolveRating(problemType string, correct bool, chosen *int) (int, error) {
	switch problemType {
	case ratingTypeMCQ, ratingTypeCloze:
		if !correct {
			return RatingAgain, nil
		}
		if chosen == nil {
			return DefaultRating, nil
		}
		if *chosen < RatingHard || *chosen > RatingEasy {
			return 0, fmt.Errorf("%w: %d not allowed for a right answer", ErrInvalidRating, *chosen)
		}
		return *chosen, nil
	case ratingTypeOpen:
		if chosen == nil {
			return 0, ErrRatingRequired
		}
		if *chosen < RatingAgain || *chosen > RatingEasy {
			return 0, fmt.Errorf("%w: %d", ErrInvalidRating, *chosen)
		}
		return *chosen, nil
	default:
		return 0, fmt.Errorf("%w: %q", ErrUnknownProblemType, problemType)
	}
}
