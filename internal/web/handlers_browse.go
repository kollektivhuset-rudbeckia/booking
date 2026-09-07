package web

import (
	"net/http"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/booking"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// resourceCard is a resource plus the one fact people want on the start page:
// when it is next free.
type resourceCard struct {
	Resource config.Resource
	Rules    ruleSummary
	NextFree *time.Time
	Busy     bool
}

// firstFree is the soonest booking a resource will take: the shortest length
// it offers, at the earliest start nothing else is in the way of. It is what
// the start page's quick button books, and what the resource page suggests
// when that time is not one the member can have straight away.
type firstFree struct {
	Start time.Time
	End   time.Time
	// Immediate is true when the booking could begin now rather than after a
	// wait. The quick button only books without asking when it is.
	Immediate bool
	// Link is the resource page with this time already chosen, ready to
	// confirm.
	Link string
}

// ruleSummary renders the booking rules as short human sentences.
type ruleSummary struct {
	Mode      config.Mode
	Durations string
	// Custom is the span of a typed-in length, e.g. "30 min – 10 h". Empty
	// when the resource only offers the preset lengths.
	Custom    string
	Window    string
	Buffer    string
	Advance   string
	Limit     string
	Nights    string
	CheckTime string
}

func summarize(lang i18n.Lang, r config.Resource) ruleSummary {
	ru := r.Rules
	s := ruleSummary{Mode: ru.Mode}
	switch ru.Mode {
	case config.ModeHours:
		s.Durations = i18n.DurationList(lang, ru.Durations)
		s.Window = ru.OpenFrom + "–" + ru.OpenTo
		if ru.CustomDuration {
			s.Custom = i18n.Duration(time.Duration(ru.MinDurationMinutes)*time.Minute) +
				" – " + i18n.Duration(time.Duration(ru.MaxDurationMinutes)*time.Minute)
		}
	case config.ModeDays:
		s.Nights = i18n.Count(lang, "night", ru.MinDays) + " – " + i18n.Count(lang, "night", ru.MaxDays)
		s.CheckTime = i18n.T(lang, "rules.checktime", ru.CheckIn, ru.CheckOut)
	}
	if ru.BufferMinutes > 0 {
		s.Buffer = i18n.Duration(time.Duration(ru.BufferMinutes) * time.Minute)
	}
	s.Advance = i18n.Days(lang, ru.MaxAdvanceDays)
	if ru.MaxActivePerUser > 0 {
		s.Limit = i18n.Count(lang, "active", ru.MaxActivePerUser)
	}
	return s
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request, v *view) {
	now := s.now()
	loc := s.cfg.Location()

	groups := s.cfg.Grouped()
	cards := make([]struct {
		Category  config.Category
		Resources []resourceCard
	}, 0, len(groups))

	for _, g := range groups {
		row := struct {
			Category  config.Category
			Resources []resourceCard
		}{Category: g.Category}
		for _, res := range g.Resources {
			card := resourceCard{Resource: res, Rules: summarize(v.Lang, res)}
			if next, ok := s.nextFree(r, res, now, loc, v.Lang); ok {
				card.NextFree = &next.Start
			} else {
				card.Busy = true
			}
			row.Resources = append(row.Resources, card)
		}
		cards = append(cards, row)
	}

	var mine []bookingRow
	if v.Ident.MMUsername != "" {
		list, err := s.store.ByMember(r.Context(), v.Ident.MMUsername, false, now)
		if err != nil {
			s.log.Error("load own bookings", "err", err)
		} else {
			mine = s.rows(v.Lang, list, loc)
			if len(mine) > 3 {
				mine = mine[:3]
			}
		}
	}

	v.Title = i18n.T(v.Lang, "nav.book")
	v.Data = map[string]any{"Groups": cards, "Mine": mine}
	s.render(w, r, http.StatusOK, "index.html", v)
}

// nextFree scans forward for the soonest time a resource will take, using the
// shortest length it allows. It looks a couple of weeks ahead and then gives
// up, which is why it reports whether it found anything at all.
func (s *Server) nextFree(r *http.Request, res config.Resource, now time.Time, loc *time.Location, lang i18n.Lang) (firstFree, bool) {
	horizon := 14
	if res.Rules.MaxAdvanceDays < horizon {
		horizon = res.Rules.MaxAdvanceDays
	}
	from := now
	to := now.AddDate(0, 0, horizon+1)
	existing, err := s.store.InRange(r.Context(), res.ID, from.Add(-24*time.Hour), to)
	if err != nil {
		s.log.Error("load bookings", "resource", res.ID, "err", err)
		return firstFree{}, false
	}

	switch res.Rules.Mode {
	case config.ModeHours:
		dur := time.Duration(res.Rules.Durations[0] * float64(time.Hour))
		// A time counts as one the member can have now on two conditions, and
		// each catches a different kind of wait.
		//
		// It has to be soon: the notice period plus the one slot step the grid
		// may have to round up to. A resource that does not open until 20:00
		// has a first free time, but not one you can take at breakfast.
		soonest := now.Add(time.Duration(res.Rules.MinNoticeMinutes+res.Rules.SlotStepMinutes) * time.Minute)
		// And nothing bookable can have gone by before it. Slots are walked in
		// time order, so passing over one that the clock allowed but somebody
		// else holds means everything after it is a wait — however short.
		earliest := now.Add(time.Duration(res.Rules.MinNoticeMinutes) * time.Minute)
		nothingSkipped := true
		for d := 0; d <= horizon; d++ {
			day := now.In(loc).AddDate(0, 0, d)
			dv := booking.BuildDay(res, day, dur, existing, now, loc, "", lang)
			for _, slot := range dv.Slots {
				if slot.Start.Before(earliest) {
					// Already gone: never a position anybody could have booked.
					continue
				}
				if !slot.Available {
					nothingSkipped = false
					continue
				}
				start := slot.Start.In(loc)
				return firstFree{
					Start:     slot.Start,
					End:       slot.End,
					Immediate: nothingSkipped && !slot.Start.After(soonest),
					Link: "/resurs/" + res.ID + "?datum=" + i18n.ISODate(start) +
						"&langd=" + booking.HoursParam(dur) +
						"&start=" + i18n.Clock(start) + "#boka",
				}, true
			}
		}
	case config.ModeDays:
		today := truncDay(now.In(loc), loc)
		cells := booking.MonthGrid(res, today, existing, now, loc, "")
		cells = append(cells, booking.MonthGrid(res, today.AddDate(0, 1, 0), existing, now, loc, "")...)
		// The grids overlap at the month boundary and reach a few days either
		// side, so read them as a set of free nights rather than a sequence.
		free := make(map[string]bool, len(cells))
		for _, c := range cells {
			if c.Available && !c.Past {
				free[i18n.ISODate(c.Date)] = true
			}
		}
		for _, c := range cells {
			if !wholeStayFree(res, c.Date, free) {
				continue
			}
			out := c.Date.AddDate(0, 0, res.Rules.MinDays)
			_, end := booking.DayRange(res, c.Date, out, loc)
			return firstFree{
				Start: c.Date,
				End:   end,
				// A room is had by the night: today is as immediate as it gets.
				Immediate: i18n.ISODate(c.Date) == i18n.ISODate(today),
				Link: "/resurs/" + res.ID + "?manad=" + c.Date.Format("2006-01") +
					"&fran=" + i18n.ISODate(c.Date) +
					"&till=" + i18n.ISODate(out) + "#boka",
			}, true
		}
	}
	return firstFree{}, false
}

// wholeStayFree reports whether the shortest stay the resource allows fits
// from this night on. One free night is not an offer when the resource asks
// for three of them.
func wholeStayFree(res config.Resource, from time.Time, free map[string]bool) bool {
	for n := 0; n < res.Rules.MinDays; n++ {
		if !free[i18n.ISODate(from.AddDate(0, 0, n))] {
			return false
		}
	}
	return true
}

// whenLabel says when a time is, the way each kind of resource is spoken
// about: "imorgon 09:00" for a bike, "imorgon" for a guest room, because a
// room is booked by the night and a clock time would be noise.
func whenLabel(lang i18n.Lang, res config.Resource, t, now time.Time, loc *time.Location) string {
	label := i18n.RelativeDay(lang, t.In(loc), now.In(loc))
	if res.Rules.Mode != config.ModeDays {
		label += " " + i18n.Clock(t.In(loc))
	}
	return label
}

// bookingRow decorates a stored booking with everything the templates show.
type bookingRow struct {
	Booking  store.Booking
	Resource config.Resource
	Known    bool
	Start    time.Time
	End      time.Time
	Duration string
	Nights   int
	Upcoming bool
	Ongoing  bool
}

func (s *Server) rows(lang i18n.Lang, list []store.Booking, loc *time.Location) []bookingRow {
	now := s.now()
	out := make([]bookingRow, 0, len(list))
	for _, b := range list {
		res, ok := s.cfg.Resource(b.ResourceID)
		if !ok {
			res = config.Resource{ID: b.ResourceID, Name: b.ResourceID}
		}
		row := bookingRow{
			Booking:  b,
			Resource: res,
			Known:    ok,
			Start:    b.Start.In(loc),
			End:      b.End.In(loc),
			Duration: i18n.Duration(b.End.Sub(b.Start)),
			Upcoming: b.Start.After(now),
			Ongoing:  !b.Start.After(now) && b.End.After(now),
		}
		if b.Mode == string(config.ModeDays) {
			sd := time.Date(row.Start.Year(), row.Start.Month(), row.Start.Day(), 0, 0, 0, 0, loc)
			ed := time.Date(row.End.Year(), row.End.Month(), row.End.Day(), 0, 0, 0, 0, loc)
			row.Nights = int(ed.Sub(sd).Hours()/24 + 0.5)
		}
		out = append(out, row)
	}
	return out
}
