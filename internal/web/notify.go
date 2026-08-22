package web

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/ical"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/mattermost"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// notifyTimeout bounds a background notification. It is generous: the member
// is already looking at their confirmation page, nobody is waiting for this.
const notifyTimeout = 30 * time.Second

// memberLang is the language to write to a member in: the one they have set in
// Mattermost, or the deployment's own when they have set nothing we know.
//
// This is not the language of the page the booking was made on. A member who
// reads the site in English while their chat is Swedish gets Swedish messages,
// because the message arrives in the chat, days later, on its terms.
func (s *Server) memberLang(u mattermost.User) i18n.Lang {
	if lang, ok := i18n.Parse(u.Locale); ok {
		return lang
	}
	return s.defaultLang()
}

// bookingLang is the same for a booking already saved. Bookings made before
// the site had languages have nothing recorded and fall back.
func (s *Server) bookingLang(b store.Booking) i18n.Lang {
	if lang, ok := i18n.Parse(b.Lang); ok {
		return lang
	}
	return s.defaultLang()
}

// notifyCreated direct-messages the confirmation, with the calendar file
// attached and a link that drops the booking into Google Calendar.
func (s *Server) notifyCreated(b store.Booking, res config.Resource) {
	loc := s.cfg.Location()
	lang := s.bookingLang(b)
	l := string(lang)
	ev := s.event(b, res, lang)
	link := s.rt.BaseURL + "/bokning/" + b.ID
	when := i18n.Interval(lang, b.Start.In(loc), b.End.In(loc))

	var m bytes.Buffer
	fmt.Fprintf(&m, "%s\n\n", i18n.T(lang, "dm.confirmed", firstName(b.Name), res.NameFor(l)))
	fmt.Fprintf(&m, "| | |\n|---|---|\n")
	fmt.Fprintf(&m, "| **%s** | %s |\n", i18n.T(lang, "dm.when"), cell(i18n.TitleCase(when)))
	if b.Mode == string(config.ModeDays) {
		fmt.Fprintf(&m, "| **%s** | %s |\n", i18n.T(lang, "dm.length"),
			i18n.Count(lang, "night", nightsBetween(b, loc)))
	} else {
		fmt.Fprintf(&m, "| **%s** | %s |\n", i18n.T(lang, "dm.length"), i18n.Duration(b.End.Sub(b.Start)))
	}
	if where := res.LocationFor(l); where != "" {
		fmt.Fprintf(&m, "| **%s** | %s |\n", i18n.T(lang, "dm.where"), cell(where))
	}
	if b.Note != "" {
		fmt.Fprintf(&m, "| **%s** | %s |\n", i18n.T(lang, "dm.note"), cell(b.Note))
	}
	m.WriteString("\n")
	if instructions := res.InstructionsFor(l); instructions != "" {
		fmt.Fprintf(&m, "> %s\n\n", strings.ReplaceAll(strings.TrimSpace(instructions), "\n", "\n> "))
	}
	fmt.Fprintf(&m, "%s\n", i18n.T(lang, "dm.links", link, ical.GoogleLink(ev)))
	fmt.Fprintf(&m, "%s\n", i18n.T(lang, "dm.attachment"))

	s.dm(b, "confirmation", m.String(), mattermost.File{
		Filename: ical.Filename(res.ID, b.Start.In(loc)),
		Data:     ical.Calendar(res.NameFor(l), []ical.Event{ev}),
	})
}

// notifyCancelled tells the member their booking is gone and hands their
// calendar a cancellation event.
func (s *Server) notifyCancelled(b store.Booking, res config.Resource) {
	loc := s.cfg.Location()
	lang := s.bookingLang(b)
	l := string(lang)
	when := i18n.Interval(lang, b.Start.In(loc), b.End.In(loc))
	ev := s.event(b, res, lang)
	ev.Cancelled = true

	msg := i18n.T(lang, "dm.cancelled", firstName(b.Name), res.NameFor(l), cell(when)) + "\n\n" +
		i18n.T(lang, "dm.nowfree", s.rt.BaseURL+"/resurs/"+res.ID) + "\n"

	s.dm(b, "cancellation", msg, mattermost.File{
		Filename: ical.Filename(res.ID, b.Start.In(loc)),
		Data:     ical.Calendar(res.NameFor(l), []ical.Event{ev}),
	})
}

// dm sends one message to the member a booking belongs to. It is called from a
// goroutine, so it owns its own context and swallows nothing quietly.
func (s *Server) dm(b store.Booking, kind, message string, files ...mattermost.File) {
	if b.MMUserID == "" {
		// Bookings made while Mattermost was unconfigured have no account to
		// reach. Say so once rather than pretending the message went out.
		s.log.Warn("no mattermost account on booking, "+kind+" not sent",
			"booking", b.ID, "member", b.MMUsername)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	if err := s.mm.DM(ctx, b.MMUserID, message, files...); err != nil {
		s.log.Error("send "+kind, "booking", b.ID, "member", b.MMUsername, "err", err)
		return
	}
	s.log.Info("notified member", "booking", b.ID, "member", b.MMUsername,
		"kind", kind, "language", b.Lang)
}

// cell escapes the pipe characters that would otherwise split a Markdown table
// cell in two. Nothing else in these messages is user-controlled markup.
func cell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func firstName(name string) string {
	if i := strings.IndexByte(name, ' '); i > 0 {
		return name[:i]
	}
	return name
}

// nightsBetween counts calendar nights, so a DST change cannot shift the total.
func nightsBetween(b store.Booking, loc *time.Location) int {
	s, e := b.Start.In(loc), b.End.In(loc)
	sd := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, loc)
	ed := time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, loc)
	return int(ed.Sub(sd).Hours()/24 + 0.5)
}
