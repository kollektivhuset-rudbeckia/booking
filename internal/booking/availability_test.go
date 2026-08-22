package booking

import (
	"testing"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// clockOf renders a time as HH:MM in its own location.
func clockOf(t time.Time) string { return t.Format("15:04") }

func stockholm(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return loc
}

func bike() config.Resource {
	return config.Resource{
		ID:   "ellastcykel",
		Name: "Ellastcykeln",
		Rules: config.Rules{
			Mode:            config.ModeHours,
			Durations:       []float64{1, 2, 4, 8},
			SlotStepMinutes: 30,
			BufferMinutes:   15,
			OpenFrom:        "06:00",
			OpenTo:          "22:00",
			MaxAdvanceDays:  30,
		},
	}
}

func at(loc *time.Location, s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBuildDaySlotGrid(t *testing.T) {
	loc := stockholm(t)
	now := at(loc, "2026-05-01 08:00")
	day := at(loc, "2026-05-02 00:00")

	view := BuildDay(bike(), day, 2*time.Hour, nil, now, loc, "", i18n.SV)

	// 06:00 to 22:00 with a 2 h booking on a 30 min grid: last start is 20:00.
	if got, want := len(view.Slots), 29; got != want {
		t.Fatalf("slot count = %d, want %d", got, want)
	}
	if got := clockOf(view.Slots[0].Start.In(loc)); got != "06:00" {
		t.Errorf("first slot starts %s, want 06:00", got)
	}
	last := view.Slots[len(view.Slots)-1]
	if got := clockOf(last.Start.In(loc)); got != "20:00" {
		t.Errorf("last slot starts %s, want 20:00", got)
	}
	if !last.End.Equal(at(loc, "2026-05-02 22:00")) {
		t.Errorf("last slot ends %v, want 22:00", last.End.In(loc))
	}
	if view.FreeCount != len(view.Slots) {
		t.Errorf("free = %d, want all %d free on an empty day", view.FreeCount, len(view.Slots))
	}
}

func TestBuildDayRespectsBuffer(t *testing.T) {
	loc := stockholm(t)
	now := at(loc, "2026-05-01 08:00")
	day := at(loc, "2026-05-02 00:00")

	existing := []store.Booking{{
		ResourceID: "ellastcykel",
		Start:      at(loc, "2026-05-02 10:00"),
		End:        at(loc, "2026-05-02 14:00"),
		Status:     store.StatusConfirmed,
		MMUsername: "anna.andersson",
	}}

	view := BuildDay(bike(), day, time.Hour, existing, now, loc, "", i18n.SV)

	free := map[string]bool{}
	for _, s := range view.Slots {
		free[clockOf(s.Start.In(loc))] = s.Available
	}
	// The booking runs 10:00–14:00 with a 15 minute buffer, so a one-hour
	// booking may not start between 09:00 and 14:00 inclusive.
	blocked := []string{"09:00", "09:30", "10:00", "12:00", "13:30", "14:00"}
	for _, c := range blocked {
		if free[c] {
			t.Errorf("slot %s should be blocked by the 10:00–14:00 booking + buffer", c)
		}
	}
	for _, c := range []string{"08:30", "14:30", "15:00"} {
		if !free[c] {
			t.Errorf("slot %s should be free", c)
		}
	}
}

func TestBuildDayMarksOwnBooking(t *testing.T) {
	loc := stockholm(t)
	now := at(loc, "2026-05-01 08:00")
	existing := []store.Booking{{
		Start:      at(loc, "2026-05-02 10:00"),
		End:        at(loc, "2026-05-02 12:00"),
		Status:     store.StatusConfirmed,
		MMUsername: "Anna.Andersson",
	}}
	view := BuildDay(bike(), at(loc, "2026-05-02 00:00"), time.Hour, existing, now, loc, "anna.andersson", i18n.SV)
	for _, s := range view.Slots {
		if clockOf(s.Start.In(loc)) == "10:00" && s.Reason != "Din bokning" {
			t.Errorf("own booking reason = %q, want %q (case-insensitive match)", s.Reason, "Din bokning")
		}
	}
	if len(view.Spans) != 1 || !view.Spans[0].Mine {
		t.Errorf("timeline span should be marked as the member's own")
	}
}

func TestBuildDayHidesPastAndTooDistant(t *testing.T) {
	loc := stockholm(t)
	now := at(loc, "2026-05-02 12:20")
	view := BuildDay(bike(), at(loc, "2026-05-02 00:00"), time.Hour, nil, now, loc, "", i18n.SV)

	for _, s := range view.Slots {
		clock := clockOf(s.Start.In(loc))
		if clock < "12:30" && s.Available {
			t.Errorf("slot %s is in the past but was offered", clock)
		}
		if clock >= "12:30" && !s.Available {
			t.Errorf("slot %s should still be available, got %q", clock, s.Reason)
		}
	}

	// A day past the horizon offers nothing.
	far := BuildDay(bike(), at(loc, "2026-07-01 00:00"), time.Hour, nil, now, loc, "", i18n.SV)
	if far.FreeCount != 0 {
		t.Errorf("free slots %d beyond the 30 day horizon, want 0", far.FreeCount)
	}
}

func room() config.Resource {
	return config.Resource{
		ID:   "gastrum-1",
		Name: "Gästrum 1",
		Rules: config.Rules{
			Mode:           config.ModeDays,
			MinDays:        1,
			MaxDays:        7,
			CheckIn:        "15:00",
			CheckOut:       "12:00",
			MaxAdvanceDays: 180,
		},
	}
}

func TestMonthGridMarksEveryBookedNight(t *testing.T) {
	loc := stockholm(t)
	now := at(loc, "2026-05-01 09:00")
	// Check in on the 10th, out on the 13th: the nights of 10, 11 and 12.
	existing := []store.Booking{{
		Start:      at(loc, "2026-05-10 15:00"),
		End:        at(loc, "2026-05-13 12:00"),
		Status:     store.StatusConfirmed,
		MMUsername: "anna.andersson",
	}}

	cells := MonthGrid(room(), at(loc, "2026-05-01 00:00"), existing, now, loc, "anna.andersson")

	taken := map[string]bool{}
	for _, c := range cells {
		if c.InMonth && c.Taken() {
			taken[c.Date.Format("2006-01-02")] = true
			if !c.Mine {
				t.Errorf("%s should be marked as the member's own night", c.Date.Format("2006-01-02"))
			}
		}
	}
	for _, d := range []string{"2026-05-10", "2026-05-11", "2026-05-12"} {
		if !taken[d] {
			t.Errorf("night %s should be taken", d)
		}
	}
	// The check-out day is free for the next guest to check in.
	if taken["2026-05-13"] {
		t.Error("2026-05-13 is a check-out day and should be bookable")
	}
	if taken["2026-05-09"] {
		t.Error("2026-05-09 is before check-in and should be bookable")
	}
}

func TestMonthGridSeparatesPastFromTaken(t *testing.T) {
	loc := stockholm(t)
	now := at(loc, "2026-05-18 09:00")
	cells := MonthGrid(room(), at(loc, "2026-05-01 00:00"), nil, now, loc, "")

	for _, c := range cells {
		if !c.InMonth {
			continue
		}
		day := c.Date.Day()
		switch {
		case day < 18:
			if !c.Past || c.Taken() || c.Available {
				t.Errorf("%s should be past and unavailable, not booked", c.Date.Format("2006-01-02"))
			}
		default:
			if !c.Available {
				t.Errorf("%s should be available", c.Date.Format("2006-01-02"))
			}
		}
	}
}

func TestDayRangeUsesCheckTimes(t *testing.T) {
	loc := stockholm(t)
	start, end := DayRange(room(), at(loc, "2026-05-10 00:00"), at(loc, "2026-05-13 00:00"), loc)
	if got := start.In(loc).Format("2006-01-02 15:04"); got != "2026-05-10 15:00" {
		t.Errorf("start = %s, want 2026-05-10 15:00", got)
	}
	if got := end.In(loc).Format("2006-01-02 15:04"); got != "2026-05-13 12:00" {
		t.Errorf("end = %s, want 2026-05-13 12:00", got)
	}
}

// A booking that spans the spring DST change must still be three nights, not
// three nights minus an hour rounded down.
func TestDayRangeAcrossDSTChange(t *testing.T) {
	loc := stockholm(t)
	// Sweden moves to summer time on 2026-03-29.
	start, end := DayRange(room(), at(loc, "2026-03-28 00:00"), at(loc, "2026-03-31 00:00"), loc)
	hours := end.Sub(start).Hours()
	if hours >= 69 {
		t.Errorf("expected the DST hour to be lost from the wall-clock span, got %v h", hours)
	}
	if got := start.In(loc).Format("15:04"); got != "15:00" {
		t.Errorf("check-in drifted to %s", got)
	}
	if got := end.In(loc).Format("15:04"); got != "12:00" {
		t.Errorf("check-out drifted to %s", got)
	}
}

// Opening hours follow the wall clock. Sweden changes the clocks at 02:00/03:00,
// so building the window by adding six hours to midnight lands on 07:00 in
// spring and 05:00 in autumn — the resource would look like it opened at the
// wrong time on exactly those two days.
func TestOpeningHoursSurviveDSTChanges(t *testing.T) {
	loc := stockholm(t)
	res := bike() // 06:00–22:00

	days := map[string]string{
		"2026-03-28": "the day before the clocks go forward",
		"2026-03-29": "the clocks go forward at 03:00",
		"2026-10-25": "the clocks go back at 02:00",
		"2026-10-26": "the day after the clocks go back",
	}
	for day, note := range days {
		from, to := dayWindow(res, at(loc, day+" 00:00"), loc)
		if got := clockOf(from.In(loc)); got != "06:00" {
			t.Errorf("%s (%s): opens %s, want 06:00", day, note, got)
		}
		if got := clockOf(to.In(loc)); got != "22:00" {
			t.Errorf("%s (%s): closes %s, want 22:00", day, note, got)
		}
		// The change happens before the window opens, so the bookable part of
		// the day is a full sixteen hours even on a 23 or 25 hour day.
		if got := to.Sub(from); got != 16*time.Hour {
			t.Errorf("%s (%s): real span %v, want 16h", day, note, got)
		}
	}
}

// A booking as long as the whole window therefore fits on every day, including
// the ones where the clocks change.
func TestFullDayLengthFitsOnDSTDays(t *testing.T) {
	loc := stockholm(t)
	res := bike()

	for _, day := range []string{"2026-03-28", "2026-03-29", "2026-10-25"} {
		// Stand a few days before each one, so nothing falls outside the
		// resource's booking horizon.
		now := at(loc, day+" 00:00").AddDate(0, 0, -5)
		view := BuildDay(res, at(loc, day+" 00:00"), 16*time.Hour, nil, now, loc, "", i18n.SV)
		if view.FreeCount != 1 {
			t.Errorf("%s: %d starts for a 16 h booking, want exactly 1", day, view.FreeCount)
			continue
		}
		slot := view.Slots[0]
		if clockOf(slot.Start.In(loc)) != "06:00" || clockOf(slot.End.In(loc)) != "22:00" {
			t.Errorf("%s: the full-day slot runs %s–%s, want 06:00–22:00",
				day, clockOf(slot.Start.In(loc)), clockOf(slot.End.In(loc)))
		}
	}
}
