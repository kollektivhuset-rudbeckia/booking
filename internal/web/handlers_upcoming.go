package web

import (
	"net/http"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
)

// dayGroup is one calendar day's worth of bookings on the upcoming list.
type dayGroup struct {
	Date  time.Time
	Rows  []bookingRow
	Today bool
}

// handleResourceUpcoming lists a resource's coming bookings in time order,
// starting from right now. Anything already finished is left out — the point
// of the page is "when is it free next", not a history.
func (s *Server) handleResourceUpcoming(w http.ResponseWriter, r *http.Request, v *view) {
	res, ok := s.cfg.Resource(r.PathValue("id"))
	if !ok {
		s.errorPage(w, r, http.StatusNotFound, "error.noresource", "error.checklink.home")
		return
	}
	now := s.now()
	loc := s.cfg.Location()

	// Reach a day past the booking horizon so nothing configured can fall off
	// the end of the list.
	list, err := s.store.InRange(r.Context(), res.ID, now, now.AddDate(0, 0, res.Rules.MaxAdvanceDays+1))
	if err != nil {
		s.log.Error("load upcoming", "resource", res.ID, "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.tryagain")
		return
	}

	rows := s.rows(v.Lang, list, loc)
	today := truncDay(now.In(loc), loc)

	// InRange returns them in start order, so one pass groups them by day.
	var groups []dayGroup
	for _, row := range rows {
		day := truncDay(row.Start, loc)
		if n := len(groups); n > 0 && groups[n-1].Date.Equal(day) {
			groups[n-1].Rows = append(groups[n-1].Rows, row)
			continue
		}
		groups = append(groups, dayGroup{Date: day, Rows: []bookingRow{row}, Today: day.Equal(today)})
	}

	// "imorgon 09:00" for a bike, "imorgon" for a guest room: a room is booked
	// by the night, so a clock time would be noise.
	nextFreeLabel := ""
	if t, ok := s.nextFree(r, res, now, loc, v.Lang); ok {
		nextFreeLabel = i18n.RelativeDay(v.Lang, t.In(loc), now.In(loc))
		if res.Rules.Mode != config.ModeDays {
			nextFreeLabel += " " + i18n.Clock(t.In(loc))
		}
	}

	v.Title = i18n.T(v.Lang, "upcoming.title") + " – " + res.NameFor(string(v.Lang))
	v.Data = map[string]any{
		"Resource":      res,
		"Rules":         summarize(v.Lang, res),
		"Groups":        groups,
		"Count":         len(rows),
		"NextFreeLabel": nextFreeLabel,
		"Feed":          "/kalender/" + res.ID + "/flode.ics",
		"IsDays":        res.Rules.Mode == config.ModeDays,
	}
	s.render(w, r, http.StatusOK, "upcoming.html", v)
}
