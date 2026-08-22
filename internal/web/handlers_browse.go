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
			if t, ok := s.nextFree(r, res, now, loc, v.Lang); ok {
				card.NextFree = &t
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

// nextFree scans forward for the first bookable start time, using the shortest
// allowed duration. It looks a couple of weeks ahead and then gives up.
func (s *Server) nextFree(r *http.Request, res config.Resource, now time.Time, loc *time.Location, lang i18n.Lang) (time.Time, bool) {
	horizon := 14
	if res.Rules.MaxAdvanceDays < horizon {
		horizon = res.Rules.MaxAdvanceDays
	}
	from := now
	to := now.AddDate(0, 0, horizon+1)
	existing, err := s.store.InRange(r.Context(), res.ID, from.Add(-24*time.Hour), to)
	if err != nil {
		s.log.Error("load bookings", "resource", res.ID, "err", err)
		return time.Time{}, false
	}

	switch res.Rules.Mode {
	case config.ModeHours:
		dur := time.Duration(res.Rules.Durations[0] * float64(time.Hour))
		for d := 0; d <= horizon; d++ {
			day := now.In(loc).AddDate(0, 0, d)
			dv := booking.BuildDay(res, day, dur, existing, now, loc, "", lang)
			for _, slot := range dv.Slots {
				if slot.Available {
					return slot.Start, true
				}
			}
		}
	case config.ModeDays:
		today := now.In(loc)
		cells := booking.MonthGrid(res, today, existing, now, loc, "")
		next := booking.MonthGrid(res, today.AddDate(0, 1, 0), existing, now, loc, "")
		for _, c := range append(cells, next...) {
			if c.Available && !c.Past {
				return c.Date, true
			}
		}
	}
	return time.Time{}, false
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
