// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"sort"
	"testing"
	"time"
)

func TestIDsSortByTime(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	gen := NewIDGenerator(clock)
	var ids []string
	for i := 0; i < 50; i++ {
		ids = append(ids, gen.New().String())
		clock.Advance(time.Millisecond)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("IDs generated at increasing times are not lexicographically sorted")
	}
}

func TestIDsMonotonicWithinMillisecondAndBackwardsClock(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	gen := NewIDGenerator(clock)
	prev := gen.New().String()
	for i := 0; i < 10000; i++ {
		if i == 5000 {
			clock.Advance(-time.Hour)
		}
		cur := gen.New().String()
		if cur <= prev {
			t.Fatalf("not strictly increasing at %d: %s <= %s", i, cur, prev)
		}
		prev = cur
	}
}

func TestIDFormat(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 123_000_000, time.UTC)
	id := NewIDGenerator(NewFakeClock(at)).New()
	s := id.String()
	if len(s) != 36 || s[14] != '7' {
		t.Fatalf("bad version/format: %s", s)
	}
	if v := s[19]; v != '8' && v != '9' && v != 'a' && v != 'b' {
		t.Fatalf("bad variant: %s", s)
	}
	if !id.Time().Equal(at) {
		t.Fatalf("Time() = %v, want %v", id.Time(), at)
	}
	back, err := ParseID(s)
	if err != nil || back != id {
		t.Fatalf("round trip failed: %v %v", back, err)
	}
	for _, bad := range []string{"", "nope", s[:35], s + "0", "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) succeeded", bad)
		}
	}
}

func TestFakeClock(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	c := NewFakeClock(start)
	if c.Now().Location() != time.UTC || !c.Now().Equal(start) {
		t.Fatalf("Now() = %v", c.Now())
	}
	c.Advance(time.Minute)
	if got := c.Now().Sub(start); got != time.Minute {
		t.Fatalf("advanced %v", got)
	}
	var _ Clock = SystemClock{}
	if (SystemClock{}).Now().Location() != time.UTC {
		t.Fatal("system clock not UTC")
	}
}

func TestActorValidate(t *testing.T) {
	uid := NewIDGenerator(SystemClock{}).New()
	ok := []Actor{
		{Kind: ActorHuman, UserID: uid, ClientID: "web"},
		{Kind: ActorAgent, UserID: uid, ClientID: "mcp"},
		{Kind: ActorSystem},
	}
	for _, a := range ok {
		if err := a.Validate(); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	bad := []Actor{{Kind: "robot", UserID: uid}, {Kind: ActorHuman}, {Kind: ActorAgent}}
	for _, a := range bad {
		if a.Validate() == nil {
			t.Errorf("%+v should be invalid", a)
		}
	}
}
