// SPDX-License-Identifier: Apache-2.0

package fsrs

import (
	"fmt"
	"math"
	"time"
)

// Rating is the user's answer grade for one review.
type Rating int

// Ratings, in FSRS order.
const (
	Again Rating = 1
	Hard  Rating = 2
	Good  Rating = 3
	Easy  Rating = 4
)

// Valid reports whether r is one of Again, Hard, Good or Easy.
func (r Rating) Valid() bool { return r >= Again && r <= Easy }

// State is a card's scheduling state.
type State string

// Card states. The empty State is treated as New.
const (
	New        State = "new"
	Learning   State = "learning"
	Review     State = "review"
	Relearning State = "relearning"
)

// Card is one user's scheduling state for one problem. The zero Card is a
// new card. Cards are values; Next never mutates its argument.
type Card struct {
	State      State
	Step       int       // index into the learning or relearning steps; meaningful in those states only
	Stability  float64   // days; 0 until the first review
	Difficulty float64   // 1..10; 0 until the first review
	Due        time.Time // when the card is next due
	LastReview time.Time // zero until the first review
}

const day = 24 * time.Hour

// Params are the scheduler inputs. Start from Default, override fields (for
// example DesiredRetention), then call Validate.
type Params struct {
	// W holds the 21 FSRS-6 weights.
	W [21]float64
	// DesiredRetention is the target recall probability at review time, in (0,1).
	DesiredRetention float64
	// LearningSteps and RelearningSteps are the same-day step intervals.
	LearningSteps   []time.Duration
	RelearningSteps []time.Duration
	// MaxIntervalDays caps review intervals.
	MaxIntervalDays int
}

// DefaultWeights are the published FSRS-6 default parameters.
var DefaultWeights = [21]float64{
	0.212, 1.2931, 2.3065, 8.2956, 6.4133, 0.8334, 3.0194, 0.001, 1.8722,
	0.1666, 0.796, 1.4835, 0.0614, 0.2629, 1.6483, 0.6014, 1.8729, 0.5425,
	0.0912, 0.0658, 0.1542,
}

var (
	lowerBounds = [21]float64{0.001, 0.001, 0.001, 0.001, 1, 0.001, 0.001, 0.001, 0, 0, 0.001, 0.001, 0.001, 0.001, 0, 0, 1, 0, 0, 0, 0.1}
	upperBounds = [21]float64{100, 100, 100, 100, 10, 4, 4, 0.75, 4.5, 0.8, 3.5, 5, 0.25, 0.9, 4, 1, 6, 2, 2, 0.8, 0.8}
)

// Default returns the default parameters: FSRS-6 weights, desired retention
// 0.90, learning and relearning steps of 1 m and 10 m, 36500-day cap.
func Default() Params {
	return Params{
		W:                DefaultWeights,
		DesiredRetention: 0.90,
		LearningSteps:    []time.Duration{time.Minute, 10 * time.Minute},
		RelearningSteps:  []time.Duration{time.Minute, 10 * time.Minute},
		MaxIntervalDays:  36500,
	}
}

// Validate checks that the parameters are usable.
func (p Params) Validate() error {
	for i, w := range p.W {
		if math.IsNaN(w) || w < lowerBounds[i] || w > upperBounds[i] {
			return fmt.Errorf("fsrs: weight %d = %v outside [%v, %v]", i, w, lowerBounds[i], upperBounds[i])
		}
	}
	if !(p.DesiredRetention > 0 && p.DesiredRetention < 1) {
		return fmt.Errorf("fsrs: desired retention %v outside (0,1)", p.DesiredRetention)
	}
	if p.MaxIntervalDays < 1 {
		return fmt.Errorf("fsrs: max interval %d < 1 day", p.MaxIntervalDays)
	}
	for _, steps := range [][]time.Duration{p.LearningSteps, p.RelearningSteps} {
		for _, s := range steps {
			if s <= 0 {
				return fmt.Errorf("fsrs: step %v must be positive", s)
			}
		}
	}
	return nil
}

func (p Params) decay() float64  { return -p.W[20] }
func (p Params) factor() float64 { return math.Pow(0.9, 1/p.decay()) - 1 }

// elapsedDays is the whole number of days from last to now, floored (so a
// negative span is negative).
func elapsedDays(last, now time.Time) int64 {
	d := now.Sub(last)
	n := int64(d / day)
	if d%day < 0 {
		n--
	}
	return n
}

// Retrievability is the predicted probability of recalling c at now, using
// whole elapsed days. It is 0 for a card that has never been reviewed.
func (p Params) Retrievability(c Card, now time.Time) float64 {
	if c.LastReview.IsZero() || c.Stability <= 0 {
		return 0
	}
	days := math.Max(0, float64(elapsedDays(c.LastReview, now)))
	return math.Pow(1+p.factor()*days/c.Stability, p.decay())
}

func clampStability(s float64) float64 { return math.Max(s, 0.001) }

func clampDifficulty(d float64) float64 { return math.Min(math.Max(d, 1), 10) }

func (p Params) initialStability(r Rating) float64 { return clampStability(p.W[r-1]) }

func (p Params) initialDifficulty(r Rating, clamp bool) float64 {
	d := p.W[4] - math.Exp(p.W[5]*float64(r-1)) + 1
	if clamp {
		d = clampDifficulty(d)
	}
	return d
}

// nextInterval is the review interval for a stability. Rounding is half to
// even to match the reference (Python's round).
func (p Params) nextInterval(stability float64) time.Duration {
	iv := stability / p.factor() * (math.Pow(p.DesiredRetention, 1/p.decay()) - 1)
	days := int(math.RoundToEven(iv))
	days = max(days, 1)
	days = min(days, p.MaxIntervalDays)
	return time.Duration(days) * day
}

func (p Params) shortTermStability(s float64, r Rating) float64 {
	inc := math.Exp(p.W[17]*(float64(r)-3+p.W[18])) * math.Pow(s, -p.W[19])
	if r >= Hard {
		inc = math.Max(inc, 1)
	}
	return clampStability(s * inc)
}

func (p Params) nextDifficulty(d float64, r Rating) float64 {
	arg1 := p.initialDifficulty(Easy, false)
	delta := -(p.W[6] * float64(r-3))
	arg2 := d + (10-d)*delta/9
	return clampDifficulty(p.W[7]*arg1 + (1-p.W[7])*arg2)
}

func (p Params) nextStability(d, s, retr float64, r Rating) float64 {
	var next float64
	if r == Again {
		long := p.W[11] * math.Pow(d, -p.W[12]) * (math.Pow(s+1, p.W[13]) - 1) * math.Exp((1-retr)*p.W[14])
		short := s / math.Exp(p.W[17]*p.W[18])
		next = math.Min(long, short)
	} else {
		hard, easy := 1.0, 1.0
		if r == Hard {
			hard = p.W[15]
		}
		if r == Easy {
			easy = p.W[16]
		}
		next = s * (1 + math.Exp(p.W[8])*(11-d)*math.Pow(s, -p.W[9])*(math.Exp((1-retr)*p.W[10])-1)*hard*easy)
	}
	return clampStability(next)
}

// Next returns the card after a review with the given rating at now. It is
// a pure function of its arguments. An invalid rating returns c unchanged;
// callers validate ratings with Rating.Valid.
func (p Params) Next(c Card, r Rating, now time.Time) Card {
	if !r.Valid() {
		return c
	}
	state := c.State
	if state == "" || state == New {
		state = Learning
		c.Step = 0
	}

	// Under one whole day since the last review (including a review dated
	// before it) uses the short-term stability formula.
	sameDay := !c.LastReview.IsZero() && elapsedDays(c.LastReview, now) < 1
	hasHistory := c.Stability > 0 && c.Difficulty > 0

	switch {
	case state == Learning && !hasHistory:
		c.Stability = p.initialStability(r)
		c.Difficulty = p.initialDifficulty(r, true)
	case sameDay:
		c.Stability = p.shortTermStability(c.Stability, r)
		c.Difficulty = p.nextDifficulty(c.Difficulty, r)
	default:
		c.Stability = p.nextStability(c.Difficulty, c.Stability, p.Retrievability(c, now), r)
		c.Difficulty = p.nextDifficulty(c.Difficulty, r)
	}

	var interval time.Duration
	if state == Review {
		if r == Again && len(p.RelearningSteps) > 0 {
			c.State = Relearning
			c.Step = 0
			interval = p.RelearningSteps[0]
		} else {
			c.State = Review
			c.Step = 0
			interval = p.nextInterval(c.Stability)
		}
	} else {
		steps := p.LearningSteps
		if state == Relearning {
			steps = p.RelearningSteps
		}
		c.State = state
		interval = p.stepInterval(&c, r, steps)
	}

	c.Due = now.Add(interval)
	c.LastReview = now
	return c
}

// stepInterval advances a Learning or Relearning card through its steps and
// returns the next interval, graduating to Review where the reference does.
func (p Params) stepInterval(c *Card, r Rating, steps []time.Duration) time.Duration {
	graduate := func() time.Duration {
		c.State = Review
		c.Step = 0
		return p.nextInterval(c.Stability)
	}
	if len(steps) == 0 || (c.Step >= len(steps) && r != Again) {
		return graduate()
	}
	switch r {
	case Again:
		c.Step = 0
		return steps[0]
	case Hard:
		switch {
		case c.Step == 0 && len(steps) == 1:
			return steps[0] * 3 / 2
		case c.Step == 0:
			return (steps[0] + steps[1]) / 2
		default:
			return steps[c.Step]
		}
	case Good:
		if c.Step+1 == len(steps) {
			return graduate()
		}
		c.Step++
		return steps[c.Step]
	default: // Easy
		return graduate()
	}
}

// Next schedules c with the default parameters.
func Next(c Card, r Rating, now time.Time) Card { return Default().Next(c, r, now) }

// Retrievability computes recall probability with the default parameters.
func Retrievability(c Card, now time.Time) float64 { return Default().Retrievability(c, now) }
