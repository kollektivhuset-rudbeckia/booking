package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/auth"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/booking"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/ical"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

func (s *Server) handleCreateBooking(w http.ResponseWriter, r *http.Request, v *view) {
	res, ok := s.cfg.Resource(r.PathValue("id"))
	if !ok || !res.Active() {
		s.errorPage(w, r, http.StatusNotFound, "error.noresource", "error.checklink")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	now := s.now()
	loc := s.cfg.Location()

	form := bookingForm{
		Name:      strings.TrimSpace(r.FormValue("name")),
		Apartment: strings.TrimSpace(r.FormValue("apartment")),
		// Kept as typed: the resolver understands names as well as usernames,
		// and if it fails the member sees their own words back in the field.
		Member:   strings.TrimSpace(r.FormValue("medlem")),
		Phone:    strings.TrimSpace(r.FormValue("phone")),
		Note:     strings.TrimSpace(r.FormValue("note")),
		Remember: r.FormValue("remember") != "",
	}

	// Who is booking? The form names a Mattermost account; everything else
	// about the member comes from there, including where to send the
	// confirmation.
	who, memberErrs := s.resolveMember(r.Context(), v.Lang, form.Member)
	if form.Name == "" {
		form.Name = who.DisplayName()
	}

	start, end, parseErrs := s.parseInterval(res, r, loc, v.Lang)
	errs := append(memberErrs, parseErrs...)
	if len(errs) == 0 {
		req := booking.Request{
			Resource: res, Start: start, End: end,
			Name: form.Name, Apartment: form.Apartment,
			MMUsername: who.Username, MMUserID: who.ID,
			Email: who.Email, Phone: form.Phone, Note: form.Note,
		}
		blockFrom, blockTo, verrs := booking.Validate(r.Context(), s.store, req, now, loc, v.Lang)
		errs = verrs
		if len(errs) == 0 {
			b := store.Booking{
				ID:          auth.ID(),
				ResourceID:  res.ID,
				Start:       start,
				End:         end,
				Mode:        string(res.Rules.Mode),
				Name:        form.Name,
				Apartment:   form.Apartment,
				MMUsername:  who.Username,
				MMUserID:    who.ID,
				Email:       who.Email,
				Lang:        string(s.memberLang(who)),
				Phone:       form.Phone,
				Note:        form.Note,
				Status:      store.StatusConfirmed,
				CancelToken: auth.Token(),
				CreatedAt:   now,
				CreatedIP:   s.clientIP(r),
			}
			err := s.store.Create(r.Context(), b, blockFrom, blockTo)
			switch {
			case errors.Is(err, store.ErrConflict):
				errs = []booking.Error{{Field: "time", Message: i18n.T(v.Lang, "error.racelost")}}
			case err != nil:
				s.log.Error("create booking", "resource", res.ID, "err", err)
				errs = []booking.Error{{Message: i18n.T(v.Lang, "error.notsaved")}}
			default:
				if form.Remember {
					s.guard.RememberIdentity(w, auth.Identity{
						Name: form.Name, Apartment: form.Apartment,
						MMUsername: who.Username, MMUserID: who.ID,
						Phone: form.Phone,
					})
				}
				s.log.Info("booking created", "id", b.ID, "resource", res.ID,
					"start", b.Start, "end", b.End, "member", b.MMUsername)
				go s.notifyCreated(b, res)
				http.Redirect(w, r, "/bokning/"+b.ID+"?ny=1", http.StatusSeeOther)
				return
			}
		}
	}

	// Something was wrong: re-render the page with the errors and the form data.
	v.Title = res.NameFor(string(v.Lang))
	data := map[string]any{"Resource": res, "Rules": summarize(v.Lang, res), "Errors": errs, "Form": form}
	switch res.Rules.Mode {
	case config.ModeHours:
		// buildHourPage reads the posted fields, so the member's day, length
		// and start time survive the round trip and the form stays open.
		page, err := s.buildHourPage(r, res, now, loc, form.Member, v.Lang)
		if err == nil {
			data["Hours"] = page
		}
		v.Data = data
		s.render(w, r, http.StatusUnprocessableEntity, "resource_hours.html", v)
	case config.ModeDays:
		page, err := s.buildDayPage(r, res, now, loc, form.Member, v.Lang)
		if err == nil {
			data["Days"] = page
		}
		v.Data = data
		s.render(w, r, http.StatusUnprocessableEntity, "resource_days.html", v)
	}
}

// parseInterval turns the posted fields into a concrete time interval.
func (s *Server) parseInterval(res config.Resource, r *http.Request, loc *time.Location, lang i18n.Lang) (time.Time, time.Time, []booking.Error) {
	switch res.Rules.Mode {
	case config.ModeHours:
		date := r.FormValue("datum")
		clock := r.FormValue("start")
		dur, err := booking.ParseHours(r.FormValue("langd"))
		if err != nil {
			return time.Time{}, time.Time{}, []booking.Error{{Field: "duration", Message: i18n.T(lang, "error.pickhowlong")}}
		}
		start, err := time.ParseInLocation("2006-01-02 15:04", date+" "+clock, loc)
		if err != nil {
			return time.Time{}, time.Time{}, []booking.Error{{Field: "time", Message: i18n.T(lang, "error.pickstart")}}
		}
		return start, start.Add(dur), nil
	case config.ModeDays:
		from, errA := time.ParseInLocation("2006-01-02", r.FormValue("fran"), loc)
		to, errB := time.ParseInLocation("2006-01-02", r.FormValue("till"), loc)
		if errA != nil || errB != nil {
			return time.Time{}, time.Time{}, []booking.Error{{Field: "time", Message: i18n.T(lang, "error.pickdates")}}
		}
		start, end := booking.DayRange(res, from, to, loc)
		return start, end, nil
	}
	return time.Time{}, time.Time{}, []booking.Error{{Message: i18n.T(lang, "error.unknownmode")}}
}

func (s *Server) handleBooking(w http.ResponseWriter, r *http.Request, v *view) {
	b, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.errorPage(w, r, http.StatusNotFound, "error.nobooking", "error.oldlink")
		return
	}
	res, known := s.cfg.Resource(b.ResourceID)
	if !known {
		res = config.Resource{ID: b.ResourceID, Name: b.ResourceID}
	}
	loc := s.cfg.Location()
	ev := s.event(b, res, v.Lang)

	rows := s.rows(v.Lang, []store.Booking{b}, loc)
	v.Title = i18n.T(v.Lang, "booking.title") + " – " + res.NameFor(string(v.Lang))
	v.Data = map[string]any{
		"Row":         rows[0],
		"New":         r.URL.Query().Get("ny") == "1",
		"Google":      ical.GoogleLink(ev),
		"Outlook":     ical.OutlookLink(ev),
		"ICS":         "/bokning/" + b.ID + "/kalender.ics",
		"CancelToken": b.CancelToken,
		"Mine":        b.MMUsername != "" && b.MMUsername == store.Member(v.Ident.MMUsername),
	}
	s.render(w, r, http.StatusOK, "booking.html", v)
}

func (s *Server) handleBookingICS(w http.ResponseWriter, r *http.Request, v *view) {
	b, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	res, ok := s.cfg.Resource(b.ResourceID)
	if !ok {
		res = config.Resource{ID: b.ResourceID, Name: b.ResourceID}
	}
	lang := s.lang(r)
	data := ical.Calendar(res.NameFor(string(lang)), []ical.Event{s.event(b, res, lang)})
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+
		ical.Filename(res.ID, b.Start.In(s.cfg.Location()))+"\"")
	w.Write(data)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request, v *view) {
	id := r.PathValue("id")
	b, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.errorPage(w, r, http.StatusNotFound, "error.nobooking", "error.alreadygone")
		return
	}
	token := r.FormValue("token")
	// A member may cancel with the token from their confirmation, or because
	// the booking belongs to the Mattermost account this browser remembers.
	own := v.Ident.MMUsername != "" && store.Member(v.Ident.MMUsername) == b.MMUsername
	if token != b.CancelToken && !own && !v.Role.Admin() {
		s.errorPage(w, r, http.StatusForbidden, "error.nocancel", "error.nocancel.how")
		return
	}
	if err := s.store.Cancel(r.Context(), id, b.CancelToken, true, s.now()); err != nil {
		s.errorPage(w, r, http.StatusConflict, "error.nocancel", "error.nocancel.done")
		return
	}
	res, ok := s.cfg.Resource(b.ResourceID)
	if !ok {
		res = config.Resource{ID: b.ResourceID, Name: b.ResourceID}
	}
	b.Status = store.StatusCancelled
	s.log.Info("booking cancelled", "id", b.ID, "resource", b.ResourceID)
	go s.notifyCancelled(b, res)
	http.Redirect(w, r, "/mina?avbokad="+id, http.StatusSeeOther)
}

func (s *Server) handleMyBookings(w http.ResponseWriter, r *http.Request, v *view) {
	loc := s.cfg.Location()

	// Whose bookings? The remembered account by default, or whoever the field
	// names. That is looked up the same way the booking form does it, so a
	// name, a nickname or half a name finds the person rather than quietly
	// finding nobody.
	member := store.Member(v.Ident.MMUsername)
	name, typed, lookupErr := v.Ident.Name, member, ""
	if q := strings.TrimSpace(r.URL.Query().Get("medlem")); q != "" {
		typed = q
		who, errs := s.findMember(r.Context(), v.Lang, q, false)
		if len(errs) > 0 {
			member, lookupErr = "", errs[0].Message
		} else {
			member, name, typed = store.Member(who.Username), who.DisplayName(), who.Username
		}
	}

	var rows []bookingRow
	if member != "" {
		list, err := s.store.ByMember(r.Context(), member, false, s.now().AddDate(0, 0, -30))
		if err != nil {
			s.log.Error("load own bookings", "err", err)
		} else {
			rows = s.rows(v.Lang, list, loc)
		}
	}
	v.Title = i18n.T(v.Lang, "mine.title")
	v.Data = map[string]any{
		"Rows":      rows,
		"Member":    member,
		"Name":      name,
		"Typed":     typed,
		"Error":     lookupErr,
		"Cancelled": r.URL.Query().Get("avbokad") != "",
	}
	s.render(w, r, http.StatusOK, "mine.html", v)
}

// handleResourceFeed publishes a resource's upcoming bookings as an .ics feed,
// so the house calendar can subscribe to it.
func (s *Server) handleResourceFeed(w http.ResponseWriter, r *http.Request, v *view) {
	res, ok := s.cfg.Resource(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := s.now()
	list, err := s.store.InRange(r.Context(), res.ID, now.AddDate(0, 0, -90), now.AddDate(1, 0, 0))
	if err != nil {
		http.Error(w, "could not read the bookings", http.StatusInternalServerError)
		return
	}
	// A feed is subscribed to by a calendar, not read by a browser, so it is
	// written in the deployment's own language rather than a reader's.
	lang := s.defaultLang()
	events := make([]ical.Event, 0, len(list))
	for _, b := range list {
		events = append(events, s.event(b, res, lang))
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write(ical.Calendar(s.cfg.Site.Title+" – "+res.NameFor(string(lang)), events))
}

// event renders a booking as a calendar event, in the language of whoever the
// file is being handed to.
func (s *Server) event(b store.Booking, res config.Resource, lang i18n.Lang) ical.Event {
	l := string(lang)
	desc := i18n.T(lang, "ical.bookedby", b.Name)
	if b.Apartment != "" {
		desc += " (" + i18n.T(lang, "admin.apartmentShort", b.Apartment) + ")"
	}
	if b.Note != "" {
		desc += "\n" + b.Note
	}
	if instructions := res.InstructionsFor(l); instructions != "" {
		desc += "\n\n" + instructions
	}
	desc += "\n\n" + i18n.T(lang, "ical.cancel", s.rt.BaseURL+"/bokning/"+b.ID)
	return ical.Event{
		UID:         b.ID + "@booking.rudbeckia.nu",
		Summary:     res.NameFor(l) + " – " + b.Name,
		Description: desc,
		Location:    res.LocationFor(l),
		Start:       b.Start,
		End:         b.End,
		Created:     b.CreatedAt,
		URL:         s.rt.BaseURL + "/bokning/" + b.ID,
		Cancelled:   b.Status == store.StatusCancelled,
	}
}
