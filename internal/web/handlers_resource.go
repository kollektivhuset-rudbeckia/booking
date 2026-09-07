package web

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/auth"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/booking"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
)

// dayTab is one day in the horizontal date strip.
type dayTab struct {
	Date     time.Time
	Label    string
	Weekday  string
	Selected bool
	Free     bool
	Today    bool
}

// hourPage is the resource page for a bike-style resource.
type hourPage struct {
	Day       booking.DayView
	Days      []dayTab
	Durations []durationTab
	Duration  time.Duration
	// Param is the current length as it appears in links, in hours.
	Param    string
	Selected *booking.Slot

	// AllowCustom turns on the "own length" choice beside the preset buttons.
	AllowCustom bool
	// ShowCustom is true once that choice has been picked, which is when the
	// field appears. Until then it is just one more button in the row.
	ShowCustom bool
	// CustomValue seeds the field: the current length as "2" or "2,5".
	CustomValue string
	// CustomError explains a typed length that could not be used.
	CustomError string
	// CustomParam keeps the current length in the link that opens the field,
	// so the field starts from what the member is already looking at.
	CustomParam string
	MinLabel    string
	MaxLabel    string
	StepLabel   string
}

type durationTab struct {
	// Param is the value for the "langd" query parameter, in hours.
	Param    string
	Label    string
	Selected bool
	Free     int
}

// dayPage is the resource page for a guest-room-style resource.
type dayPage struct {
	Month      time.Time
	Prev       time.Time
	Next       time.Time
	HasPrev    bool
	Cells      []booking.DayCell
	From       string
	To         string
	Nights     int
	RangeOK    bool
	RangeError string
	Start      time.Time
	End        time.Time
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request, v *view) {
	res, ok := s.cfg.Resource(r.PathValue("id"))
	if !ok || !res.Active() {
		s.errorPage(w, r, http.StatusNotFound, "error.noresource", "error.checklink.home")
		return
	}
	s.renderResource(w, r, v, res, nil, nil)
}

// handleQuickBook is the "book right away" button on a start page card. It
// finds the soonest time the resource will take and opens the confirmation
// form on it, so booking the next free hour is one click rather than a day, a
// length and a start time.
//
// It only goes straight through when that time is one the member can have now.
// A bike that is out until tomorrow morning, or a room free from Friday, stops
// here instead: the page offers the time rather than taking it, because "book
// it now" and "book it three days out" are not the same decision, and the
// button says the first.
func (s *Server) handleQuickBook(w http.ResponseWriter, r *http.Request, v *view) {
	res, ok := s.cfg.Resource(r.PathValue("id"))
	if !ok || !res.Active() {
		s.errorPage(w, r, http.StatusNotFound, "error.noresource", "error.checklink.home")
		return
	}
	now := s.now()
	loc := s.cfg.Location()

	next, found := s.nextFree(r, res, now, loc, v.Lang)
	if found && next.Immediate {
		http.Redirect(w, r, next.Link, http.StatusSeeOther)
		return
	}

	// Halted. Show the resource page as it looks on the day being suggested,
	// with the offer on top and no form open: taking it is a second click.
	halt := quickHalt{Nothing: !found}
	on := url.Values{}
	if found {
		halt.Label = whenLabel(v.Lang, res, next.Start, now, loc)
		halt.Link = next.Link
		start := next.Start.In(loc)
		switch res.Rules.Mode {
		case config.ModeHours:
			on.Set("datum", i18n.ISODate(start))
			on.Set("langd", booking.HoursParam(next.End.Sub(next.Start)))
		case config.ModeDays:
			on.Set("manad", start.Format("2006-01"))
		}
	}
	s.renderResource(w, r, v, res, map[string]any{"Halt": halt}, on)
}

// quickHalt is the offer the quick button leaves on the resource page when it
// will not book by itself.
type quickHalt struct {
	// Label is when the soonest time is, in words: "imorgon 09:00".
	Label string
	// Link books it.
	Link string
	// Nothing is true when the resource has nothing free within the horizon at
	// all, so there is no time to offer.
	Nothing bool
}

// renderResource draws a resource's booking page. extra is merged into the
// template data. on, when it is not nil, stands in for the request's own query
// parameters — which is how a halt shows the day it is suggesting rather than
// whatever day the page would otherwise have opened on.
func (s *Server) renderResource(w http.ResponseWriter, r *http.Request, v *view, res config.Resource, extra map[string]any, on url.Values) {
	now := s.now()
	loc := s.cfg.Location()
	if on != nil {
		// The page builders read the day off the request, so that is where it
		// has to be asked for. The copy keeps this request's own URL intact.
		r = r.Clone(r.Context())
		r.URL = &url.URL{Path: r.URL.Path, RawQuery: on.Encode()}
		r.Form, r.PostForm = nil, nil
	}
	v.Title = res.NameFor(string(v.Lang))
	data := map[string]any{
		"Resource": res,
		"Rules":    summarize(v.Lang, res),
		"Errors":   nil,
		"Form":     formFromIdentity(v.Ident),
	}
	for key, val := range extra {
		data[key] = val
	}

	switch res.Rules.Mode {
	case config.ModeHours:
		page, err := s.buildHourPage(r, res, now, loc, v.Ident.MMUsername, v.Lang)
		if err != nil {
			s.log.Error("build hour page", "resource", res.ID, "err", err)
			s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.tryagain")
			return
		}
		data["Hours"] = page
		v.Data = data
		s.render(w, r, http.StatusOK, "resource_hours.html", v)
	case config.ModeDays:
		page, err := s.buildDayPage(r, res, now, loc, v.Ident.MMUsername, v.Lang)
		if err != nil {
			s.log.Error("build day page", "resource", res.ID, "err", err)
			s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.tryagain")
			return
		}
		data["Days"] = page
		v.Data = data
		s.render(w, r, http.StatusOK, "resource_days.html", v)
	}
}

func (s *Server) buildHourPage(r *http.Request, res config.Resource, now time.Time, loc *time.Location, me string, lang i18n.Lang) (*hourPage, error) {
	// r.Form covers both the query string and a posted body, so a submission
	// that fails validation re-renders the very day and slot the member chose.
	q := formValues(r)

	// Which length is the member looking at? A preset always works; anything
	// else is only honoured when the resource allows a typed-in length.
	dur := time.Duration(res.Rules.Durations[0] * float64(time.Hour))
	var customErr string
	if raw := q.Get("langd"); raw != "" {
		want, err := booking.ParseHours(raw)
		switch {
		case err != nil:
			if res.Rules.CustomDuration {
				customErr = i18n.T(lang, durationErrorKey(err), raw)
			}
		case booking.IsPreset(res, want):
			dur = want
		case res.Rules.CustomDuration:
			if verr := booking.CheckDuration(res, want, lang); verr != nil {
				// Keep the typed value on screen next to the complaint.
				customErr = verr.Message
			} else {
				dur = want
			}
		}
	}

	// Which day? An explicit choice always wins; otherwise open on the first
	// day that actually has something free, so nobody lands on an empty grid
	// just because today is already booked out.
	today := truncDay(now.In(loc), loc)
	day := today
	chosen := false
	if raw := q.Get("datum"); raw != "" {
		if t, err := time.ParseInLocation("2006-01-02", raw, loc); err == nil {
			day, chosen = truncDay(t, loc), true
		}
	}
	if day.Before(today) {
		day = today
	}
	limit := today.AddDate(0, 0, res.Rules.MaxAdvanceDays)
	if day.After(limit) {
		day = limit
	}
	if !chosen {
		if first, err := s.firstFreeDay(r, res, day, limit, dur, now, loc, lang); err != nil {
			return nil, err
		} else if !first.IsZero() {
			day = first
		}
	}

	// One day of bookings, widened so buffers on either side are visible.
	existing, err := s.store.InRange(r.Context(), res.ID,
		day.AddDate(0, 0, -1), day.AddDate(0, 0, 2))
	if err != nil {
		return nil, err
	}

	page := &hourPage{Duration: dur, Param: booking.HoursParam(dur)}
	page.Day = booking.BuildDay(res, day, dur, existing, now, loc, me, lang)

	// The date strip covers two weeks, or less if the resource does not reach.
	span := 14
	if res.Rules.MaxAdvanceDays+1 < span {
		span = res.Rules.MaxAdvanceDays + 1
	}
	stripFrom := day.AddDate(0, 0, -3)
	if stripFrom.Before(today) {
		stripFrom = today
	}
	stripBookings, err := s.store.InRange(r.Context(), res.ID,
		stripFrom.AddDate(0, 0, -1), stripFrom.AddDate(0, 0, span+1))
	if err != nil {
		return nil, err
	}
	for i := 0; i < span; i++ {
		d := stripFrom.AddDate(0, 0, i)
		if d.After(limit) {
			break
		}
		dv := booking.BuildDay(res, d, dur, stripBookings, now, loc, me, lang)
		page.Days = append(page.Days, dayTab{
			Date:     d,
			Label:    i18n.DateShort(lang, d),
			Weekday:  i18n.WeekdayShort(lang, d),
			Selected: i18n.ISODate(d) == i18n.ISODate(day),
			Free:     dv.FreeCount > 0,
			Today:    i18n.ISODate(d) == i18n.ISODate(today),
		})
	}

	page.AllowCustom = res.Rules.CustomDuration
	if page.AllowCustom {
		// The field opens when the member picks their own length, and stays open
		// whenever the current length is one they typed rather than a preset.
		page.ShowCustom = q.Get("egen") == "1" ||
			customErr != "" ||
			!booking.IsPreset(res, dur)
		page.MinLabel = i18n.Duration(time.Duration(res.Rules.MinDurationMinutes) * time.Minute)
		page.MaxLabel = i18n.Duration(time.Duration(res.Rules.MaxDurationMinutes) * time.Minute)
		page.StepLabel = i18n.Duration(time.Duration(res.Rules.SlotStepMinutes) * time.Minute)
		page.CustomParam = booking.HoursParam(dur)
		page.CustomError = customErr
		if customErr != "" && q.Get("langd") != "" {
			// Keep what they typed on screen beside the complaint.
			page.CustomValue = q.Get("langd")
		} else {
			page.CustomValue = booking.FormatHoursInput(dur)
		}
	}

	// How many starts each length has on this day, so the buttons can show it.
	for _, d := range res.Rules.Durations {
		cand := time.Duration(d * float64(time.Hour))
		dv := booking.BuildDay(res, day, cand, existing, now, loc, me, lang)
		page.Durations = append(page.Durations, durationTab{
			Param:    booking.HoursParam(cand),
			Label:    i18n.Duration(cand),
			Selected: cand == dur && !page.ShowCustom,
			Free:     dv.FreeCount,
		})
	}

	// A chosen start time opens the confirmation form.
	if raw := q.Get("start"); raw != "" {
		for i := range page.Day.Slots {
			slot := page.Day.Slots[i]
			if i18n.Clock(slot.Start.In(loc)) == raw && slot.Available {
				page.Selected = &page.Day.Slots[i]
				break
			}
		}
	}
	return page, nil
}

func (s *Server) buildDayPage(r *http.Request, res config.Resource, now time.Time, loc *time.Location, me string, lang i18n.Lang) (*dayPage, error) {
	q := formValues(r)
	today := truncDay(now.In(loc), loc)

	month := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
	if raw := q.Get("manad"); raw != "" {
		if t, err := time.ParseInLocation("2006-01", raw, loc); err == nil {
			month = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
		}
	}
	thisMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
	if month.Before(thisMonth) {
		month = thisMonth
	}

	existing, err := s.store.InRange(r.Context(), res.ID,
		month.AddDate(0, 0, -8), month.AddDate(0, 1, 8))
	if err != nil {
		return nil, err
	}

	page := &dayPage{
		Month:   month,
		Prev:    month.AddDate(0, -1, 0),
		Next:    month.AddDate(0, 1, 0),
		HasPrev: month.After(thisMonth),
		Cells:   booking.MonthGrid(res, month, existing, now, loc, me),
		From:    q.Get("fran"),
		To:      q.Get("till"),
	}

	if page.From != "" && page.To != "" {
		from, errA := time.ParseInLocation("2006-01-02", page.From, loc)
		to, errB := time.ParseInLocation("2006-01-02", page.To, loc)
		switch {
		case errA != nil || errB != nil:
			page.RangeError = i18n.T(lang, "error.baddates")
		case !to.After(from):
			page.RangeError = i18n.T(lang, "error.checkoutfirst")
		default:
			start, end := booking.DayRange(res, from, to, loc)
			nights := int(to.Sub(from).Hours()/24 + 0.5)
			page.Start, page.End, page.Nights = start, end, nights
			switch {
			case nights < res.Rules.MinDays:
				page.RangeError = i18n.T(lang, "error.shortest", i18n.Count(lang, "night", res.Rules.MinDays))
			case nights > res.Rules.MaxDays:
				page.RangeError = i18n.T(lang, "error.longest", i18n.Count(lang, "night", res.Rules.MaxDays))
			case start.Before(now):
				page.RangeError = i18n.T(lang, "error.past")
			case start.After(now.AddDate(0, 0, res.Rules.MaxAdvanceDays)):
				page.RangeError = i18n.T(lang, "error.toofar", i18n.Days(lang, res.Rules.MaxAdvanceDays))
			default:
				conflict, err := s.store.InRange(r.Context(), res.ID,
					start.Add(-time.Duration(res.Rules.BufferMinutes)*time.Minute),
					end.Add(time.Duration(res.Rules.BufferMinutes)*time.Minute))
				if err != nil {
					return nil, err
				}
				if len(conflict) > 0 {
					page.RangeError = i18n.T(lang, "error.nighttaken")
				} else {
					page.RangeOK = true
				}
			}
		}
	}
	return page, nil
}

// bookingForm carries the member's details back into the template when a
// submission fails validation.
type bookingForm struct {
	Name      string
	Apartment string
	// Member is the Mattermost username the form names as the booker.
	Member   string
	Phone    string
	Note     string
	Remember bool
}

// formFromIdentity pre-fills the form from the remembered cookie.
func formFromIdentity(id auth.Identity) bookingForm {
	return bookingForm{
		Name:      id.Name,
		Apartment: id.Apartment,
		Member:    id.MMUsername,
		Phone:     id.Phone,
		Remember:  true,
	}
}

// firstFreeDay looks ahead for the first day with a bookable start time, up to
// two weeks out. A zero time means it found nothing and the caller should keep
// the day it already had.
func (s *Server) firstFreeDay(r *http.Request, res config.Resource, from, limit time.Time, dur time.Duration, now time.Time, loc *time.Location, lang i18n.Lang) (time.Time, error) {
	const lookahead = 14
	to := from.AddDate(0, 0, lookahead+1)
	existing, err := s.store.InRange(r.Context(), res.ID, from.AddDate(0, 0, -1), to)
	if err != nil {
		return time.Time{}, err
	}
	for i := 0; i <= lookahead; i++ {
		day := from.AddDate(0, 0, i)
		if day.After(limit) {
			break
		}
		if booking.BuildDay(res, day, dur, existing, now, loc, "", lang).FreeCount > 0 {
			return day, nil
		}
	}
	return time.Time{}, nil
}

// durationErrorKey names the catalogue phrase for an unreadable length.
func durationErrorKey(err error) string {
	switch {
	case errors.Is(err, booking.ErrNoLength):
		return "error.nolength"
	case errors.Is(err, booking.ErrNotPositive):
		return "error.notpositive"
	default:
		return "error.notanumber"
	}
}

// formValues returns the request's combined query and body parameters.
func formValues(r *http.Request) url.Values {
	if err := r.ParseForm(); err != nil {
		return r.URL.Query()
	}
	return r.Form
}

func truncDay(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}
