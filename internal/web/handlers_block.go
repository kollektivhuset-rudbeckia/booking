package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/auth"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/booking"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// A maintenance block is how the people who look after a thing take it out
// of service: whole days, with a reason in place of a booker's name. It is
// stored as a booking of KindBlock so everything that already knows how to
// show, feed and cancel a booking shows, feeds and cancels a block too.

// canBlock reports whether the viewer may reach the block page for a
// resource. The site knows a member by the Mattermost account their browser
// remembers, so this is as trusting as the rest of the site: a caretaker who
// has never booked from this device has to make a booking first, or be the
// administrator.
func (s *Server) canBlock(v *view, res config.Resource) bool {
	return v.Role.Admin() || res.Rules.Caretaker(v.Ident.MMUsername)
}

// blockForm carries the caretaker's fields back into the page when a
// submission fails.
type blockForm struct {
	Member string
	From   string
	To     string
	Reason string
	Bump   bool
}

func (s *Server) handleBlockForm(w http.ResponseWriter, r *http.Request, v *view) {
	res, ok := s.cfg.Resource(r.PathValue("id"))
	if !ok || !res.Active() {
		s.errorPage(w, r, http.StatusNotFound, "error.noresource", "error.checklink.home")
		return
	}
	if !s.canBlock(v, res) {
		s.errorPage(w, r, http.StatusForbidden, "block.forbidden", "block.forbidden.how")
		return
	}
	form := blockForm{Member: v.Ident.MMUsername}
	extra := map[string]any{
		"New":    r.URL.Query().Get("ny") == "1",
		"Lifted": r.URL.Query().Get("havd") == "1",
	}
	s.renderBlockPage(w, r, v, res, form, nil, nil, extra, http.StatusOK)
}

func (s *Server) handleCreateBlock(w http.ResponseWriter, r *http.Request, v *view) {
	res, ok := s.cfg.Resource(r.PathValue("id"))
	if !ok || !res.Active() {
		s.errorPage(w, r, http.StatusNotFound, "error.noresource", "error.checklink.home")
		return
	}
	if !s.canBlock(v, res) {
		s.errorPage(w, r, http.StatusForbidden, "block.forbidden", "block.forbidden.how")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	now := s.now()
	loc := s.cfg.Location()
	form := blockForm{
		Member: strings.TrimSpace(r.FormValue("medlem")),
		From:   strings.TrimSpace(r.FormValue("fran")),
		To:     strings.TrimSpace(r.FormValue("till")),
		Reason: strings.TrimSpace(r.FormValue("orsak")),
		Bump:   r.FormValue("avboka") != "",
	}

	who, errs := s.resolveMember(r.Context(), v.Lang, form.Member)
	req := booking.BlockRequest{
		Resource:   res,
		Reason:     form.Reason,
		MMUsername: who.Username,
		Admin:      v.Role.Admin(),
	}
	if t, err := time.ParseInLocation("2006-01-02", form.From, loc); err == nil {
		req.From = t
	}
	if t, err := time.ParseInLocation("2006-01-02", form.To, loc); err == nil {
		req.To = t
	}
	start, end, verrs := booking.ValidateBlock(req, now, loc, v.Lang)
	errs = append(errs, verrs...)

	var conflicts []bookingRow
	if len(errs) == 0 {
		b := store.Booking{
			ID:          auth.ID(),
			ResourceID:  res.ID,
			Start:       start,
			End:         end,
			Mode:        string(res.Rules.Mode),
			Kind:        store.KindBlock,
			Name:        who.DisplayName(),
			MMUsername:  who.Username,
			MMUserID:    who.ID,
			Email:       who.Email,
			Lang:        string(s.memberLang(who)),
			Note:        form.Reason,
			Status:      store.StatusConfirmed,
			CancelToken: auth.Token(),
			CreatedAt:   now,
			CreatedIP:   s.clientIP(r),
		}
		var bumped []store.Booking
		var err error
		if form.Bump {
			bumped, err = s.store.CreateOver(r.Context(), b, now)
		} else {
			err = s.store.Create(r.Context(), b, start, end)
		}
		switch {
		case errors.Is(err, store.ErrConflict):
			// Show the caretaker what is in the way and offer to clear it.
			list, lerr := s.store.InRange(r.Context(), res.ID, start, end)
			if lerr != nil {
				s.log.Error("list conflicting bookings", "resource", res.ID, "err", lerr)
			}
			conflicts = s.rows(v.Lang, list, loc)
			errs = []booking.Error{{Field: "time", Message: i18n.T(v.Lang, "block.conflicts")}}
		case err != nil:
			s.log.Error("create block", "resource", res.ID, "err", err)
			errs = []booking.Error{{Message: i18n.T(v.Lang, "error.notsaved")}}
		default:
			s.log.Info("block created", "id", b.ID, "resource", res.ID,
				"start", b.Start, "end", b.End, "member", b.MMUsername, "bumped", len(bumped))
			for _, old := range bumped {
				s.log.Info("booking bumped by block", "id", old.ID, "block", b.ID, "member", old.MMUsername)
				go s.notifyBumped(old, res, b)
			}
			target := "/resurs/" + res.ID + "/sparra?ny=1"
			if len(bumped) > 0 {
				target += "&avbokade=" + strconv.Itoa(len(bumped))
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
	}
	s.renderBlockPage(w, r, v, res, form, errs, conflicts, nil, http.StatusUnprocessableEntity)
}

// renderBlockPage draws the block form together with the blocks already in
// place, so lifting one is on the same page as putting one in.
func (s *Server) renderBlockPage(w http.ResponseWriter, r *http.Request, v *view, res config.Resource, form blockForm, errs []booking.Error, conflicts []bookingRow, extra map[string]any, status int) {
	now := s.now()
	loc := s.cfg.Location()
	list, err := s.store.Search(r.Context(), store.Filter{
		ResourceID: res.ID, Kind: store.KindBlock, Status: store.StatusConfirmed,
		From: &now, Ascending: true,
	})
	if err != nil {
		s.log.Error("list blocks", "resource", res.ID, "err", err)
	}
	v.Title = i18n.T(v.Lang, "block.title") + " – " + res.NameFor(string(v.Lang))
	data := map[string]any{
		"Resource":  res,
		"Form":      form,
		"Errors":    errs,
		"Conflicts": conflicts,
		"Blocks":    s.rows(v.Lang, list, loc),
		"Today":     i18n.ISODate(truncDay(now.In(loc), loc)),
		"Bumped":    bumpedCount(r.URL.Query().Get("avbokade")),
	}
	for k, val := range extra {
		data[k] = val
	}
	v.Data = data
	s.render(w, r, status, "block.html", v)
}

// bumpedCount reads how many bookings the block just made cancelled, which
// the redirect carries so the page can say so. Anything odd counts as none.
func bumpedCount(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
