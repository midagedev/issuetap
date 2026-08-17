// Package clock is a deterministic clock. Same seed + same tick count →
// the same timestamps. The store advances it when it creates records.
package clock

import "time"

// Clock is a monotonic source starting at a fixed instant.
type Clock struct {
	now  time.Time
	step time.Duration
}

// DefaultStart is 2026-01-15T00:00:00+0900 — a Seoul midnight, matching the
// reference site's timeZone (Asia/Seoul) so generated Jira layouts carry +0900.
func DefaultStart() time.Time {
	loc := time.FixedZone("KST", 9*3600)
	return time.Date(2026, 1, 15, 0, 0, 0, 0, loc)
}

// New returns a clock. seed shifts the start by seed hours so two seeds
// cannot collide on generated timestamps; the fixture's explicit stamps
// are never rewritten.
func New(seed int64) *Clock {
	start := DefaultStart().Add(time.Duration(seed) * time.Hour)
	return &Clock{now: start, step: time.Minute}
}

// Now is the current instant without advancing.
func (c *Clock) Now() time.Time {
	if c == nil {
		return DefaultStart()
	}
	return c.now
}

// Tick advances by one step and returns the new instant.
func (c *Clock) Tick() time.Time {
	if c == nil {
		return DefaultStart()
	}
	c.now = c.now.Add(c.step)
	return c.now
}

// Jump moves the clock to t when t is later than the current instant, so
// records created after a fixture (or persistence) load are stamped
// later than every loaded row. Earlier t is ignored — a document is a
// pure function of its own stamps, so determinism holds.
func (c *Clock) Jump(t time.Time) {
	if c == nil {
		return
	}
	if t.After(c.now) {
		c.now = t
	}
}

// Format is the Jira Cloud timestamp layout.
func Format(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.000-0700")
}
