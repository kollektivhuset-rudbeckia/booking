// Package booking turns the configured rules plus the bookings already in the
// database into "what can I actually book right now".
package booking

import (
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// Slot is one candidate start time for an hour-mode booking.
type Slot struct {
	Start     time.Time
	End       time.Time
	Available bool
	// Reason explains an unavailable slot, in the reader's language.
	Reason string
}

// Label renders the slot as "13:00–15:00" in the given location.
func (s Slot) Label(loc *time.Location) string {
	return s.Start.In(loc).Format("15:04") + "–" + s.End.In(loc).Format("15:04")
}

// Span is an occupied stretch of a day, used to draw the timeline.
type Span struct {
	Booking store.Booking
	// OffsetPct and WidthPct position the span inside the opening hours bar.
	OffsetPct float64
	WidthPct  float64
	Mine      bool
}

// DayView is everything the resource page needs for one day in hour mode.
type DayView struct {
	Date      time.Time
	OpenFrom  time.Time
	OpenTo    time.Time
	Slots     []Slot
	Spans     []Span
	FreeCount int
	// PastPct is how much of the opening hours has already gone by, so the
	// timeline can shade it.
	PastPct float64
}

// dayWindow returns the bookable window of a local calendar day.
//
// The times are built from the calendar clock rather than by adding an offset
// to midnight: on the two days a year the clocks change, adding six hours to
// midnight lands on 07:00 or 05:00, and the resource would appear to open at
// the wrong time. Opening hours follow the wall clock, so 06:00 means 06:00.
func dayWindow(r config.Resource, day time.Time, loc *time.Location) (time.Time, time.Time) {
	from, _ := config.ParseClock(r.Rules.OpenFrom)
	to, _ := config.ParseClock(r.Rules.OpenTo)
	at := func(minutes int) time.Time {
		// time.Date normalises an hour of 24 into midnight the next day, which
		// is exactly what open_to: "24:00" means.
		return time.Date(day.Year(), day.Month(), day.Day(), minutes/60, minutes%60, 0, 0, loc)
	}
	return at(from), at(to)
}

// blocked reports whether [start, end) collides with an existing booking once
// the configured buffer is respected on both sides.
func blocked(start, end time.Time, buffer time.Duration, existing []store.Booking) (store.Booking, bool) {
	for _, b := range existing {
		if !b.Active() {
			continue
		}
		if start.Before(b.End.Add(buffer)) && end.Add(buffer).After(b.Start) {
			return b, true
		}
	}
	return store.Booking{}, false
}

// BuildDay computes the slot grid and timeline for one day and one duration.
// existing must contain the resource's confirmed bookings overlapping that day
// (plus a little margin so buffers on either side are seen). me is the viewing
// member's Mattermost username, so their own bookings can be marked as theirs,
// and lang is the language the reasons are written in.
func BuildDay(r config.Resource, day time.Time, dur time.Duration, existing []store.Booking, now time.Time, loc *time.Location, me string, lang i18n.Lang) DayView {
	openFrom, openTo := dayWindow(r, day, loc)
	buffer := time.Duration(r.Rules.BufferMinutes) * time.Minute
	step := time.Duration(r.Rules.SlotStepMinutes) * time.Minute
	earliest := now.Add(time.Duration(r.Rules.MinNoticeMinutes) * time.Minute)
	latest := now.AddDate(0, 0, r.Rules.MaxAdvanceDays)

	view := DayView{Date: openFrom, OpenFrom: openFrom, OpenTo: openTo}

	for s := openFrom; !s.Add(dur).After(openTo); s = s.Add(step) {
		e := s.Add(dur)
		slot := Slot{Start: s, End: e, Available: true}
		switch {
		case s.Before(earliest):
			slot.Available, slot.Reason = false, i18n.T(lang, "slot.past")
		case s.After(latest):
			slot.Available, slot.Reason = false, i18n.T(lang, "slot.far")
		default:
			if b, hit := blocked(s, e, buffer, existing); hit {
				slot.Available = false
				if b.MMUsername != "" && me != "" && store.Member(b.MMUsername) == store.Member(me) {
					slot.Reason = i18n.T(lang, "slot.mine")
				} else {
					slot.Reason = i18n.T(lang, "slot.taken")
				}
			}
		}
		if slot.Available {
			view.FreeCount++
		}
		view.Slots = append(view.Slots, slot)
	}

	total := openTo.Sub(openFrom).Seconds()
	if now.After(openFrom) {
		view.PastPct = 100
		if now.Before(openTo) {
			view.PastPct = now.Sub(openFrom).Seconds() / total * 100
		}
	}
	for _, b := range existing {
		if !b.Active() {
			continue
		}
		s, e := b.Start, b.End
		if s.Before(openFrom) {
			s = openFrom
		}
		if e.After(openTo) {
			e = openTo
		}
		if !e.After(s) {
			continue
		}
		view.Spans = append(view.Spans, Span{
			Booking:   b,
			OffsetPct: s.Sub(openFrom).Seconds() / total * 100,
			WidthPct:  e.Sub(s).Seconds() / total * 100,
			Mine:      me != "" && store.Member(b.MMUsername) == store.Member(me),
		})
	}
	return view
}

// DayCell is one square in the month calendar used by day-mode resources.
type DayCell struct {
	Date      time.Time
	InMonth   bool
	Past      bool
	Available bool
	Mine      bool
	Label     string
	Booking   *store.Booking
}

// Taken reports whether the night is occupied, as opposed to merely being in
// the past or beyond the booking horizon.
func (c DayCell) Taken() bool { return c.Booking != nil }

// MonthGrid builds a Monday-first calendar grid for the month containing anchor.
// A day counts as taken if any confirmed booking covers its night. me is the
// viewing member's Mattermost username.
func MonthGrid(r config.Resource, anchor time.Time, existing []store.Booking, now time.Time, loc *time.Location, me string) []DayCell {
	first := time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, loc)
	// Monday = 0.
	lead := (int(first.Weekday()) + 6) % 7
	start := first.AddDate(0, 0, -lead)
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
	latest := today.AddDate(0, 0, r.Rules.MaxAdvanceDays)

	cells := make([]DayCell, 0, 42)
	for i := 0; i < 42; i++ {
		d := start.AddDate(0, 0, i)
		cell := DayCell{
			Date:    d,
			InMonth: d.Month() == first.Month(),
			Past:    d.Before(today),
			Label:   d.Format("2"),
		}
		cell.Available = !cell.Past && !d.After(latest)
		for j := range existing {
			b := existing[j]
			if !b.Active() {
				continue
			}
			// A day-mode booking owns every night from its check-in date up to
			// but not including its check-out date. Comparing calendar dates
			// keeps that true whatever the check-in and check-out times are.
			bs := b.Start.In(loc)
			be := b.End.In(loc)
			first := time.Date(bs.Year(), bs.Month(), bs.Day(), 0, 0, 0, 0, loc)
			last := time.Date(be.Year(), be.Month(), be.Day(), 0, 0, 0, 0, loc)
			if !d.Before(first) && d.Before(last) {
				cell.Available = false
				cell.Booking = &existing[j]
				cell.Mine = me != "" && store.Member(b.MMUsername) == store.Member(me)
				break
			}
		}
		cells = append(cells, cell)
		if i >= 34 && d.After(first.AddDate(0, 1, -1)) && (i+1)%7 == 0 {
			break
		}
	}
	return cells
}

// DayRange converts two calendar dates into the actual booking interval using
// the resource's check-in and check-out times.
func DayRange(r config.Resource, from, to time.Time, loc *time.Location) (time.Time, time.Time) {
	in, _ := config.ParseClock(r.Rules.CheckIn)
	out, _ := config.ParseClock(r.Rules.CheckOut)
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc).Add(time.Duration(in) * time.Minute)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc).Add(time.Duration(out) * time.Minute)
	return start, end
}
