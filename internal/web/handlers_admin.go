package web

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

// adminFilter mirrors the query string of the admin page.
type adminFilter struct {
	Resource string
	Status   string
	Query    string
	From     string
	To       string
	Scope    string // "upcoming", "past" or "all"
}

func readFilter(r *http.Request) adminFilter {
	q := r.URL.Query()
	f := adminFilter{
		Resource: q.Get("resurs"),
		Status:   q.Get("status"),
		Query:    strings.TrimSpace(q.Get("sok")),
		From:     q.Get("fran"),
		To:       q.Get("till"),
		Scope:    q.Get("period"),
	}
	if f.Scope == "" {
		f.Scope = "upcoming"
	}
	return f
}

// toStoreFilter converts the page filter into a database query.
func (f adminFilter) toStoreFilter(now time.Time, loc *time.Location) store.Filter {
	sf := store.Filter{ResourceID: f.Resource, Query: f.Query, Limit: 1000}
	switch f.Status {
	case "confirmed":
		sf.Status = store.StatusConfirmed
	case "cancelled":
		sf.Status = store.StatusCancelled
	}
	switch f.Scope {
	case "upcoming":
		t := now
		sf.From = &t
		sf.Ascending = true
	case "past":
		t := now
		sf.To = &t
	}
	if f.From != "" {
		if t, err := time.ParseInLocation("2006-01-02", f.From, loc); err == nil {
			sf.From = &t
			sf.Ascending = true
		}
	}
	if f.To != "" {
		if t, err := time.ParseInLocation("2006-01-02", f.To, loc); err == nil {
			end := t.AddDate(0, 0, 1)
			sf.To = &end
		}
	}
	return sf
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, v *view) {
	now := s.now()
	loc := s.cfg.Location()
	f := readFilter(r)

	list, err := s.store.Search(r.Context(), f.toStoreFilter(now, loc))
	if err != nil {
		s.log.Error("admin search", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.form.detail")
		return
	}
	stats, err := s.store.Stats(r.Context(), now)
	if err != nil {
		s.log.Error("admin stats", "err", err)
	}

	// Per-resource counts for the coming 30 days.
	upcoming, err := s.store.InRange(r.Context(), "", now, now.AddDate(0, 0, 30))
	if err != nil {
		s.log.Error("admin upcoming", "err", err)
	}
	counts := map[string]int{}
	hours := map[string]float64{}
	for _, b := range upcoming {
		counts[b.ResourceID]++
		hours[b.ResourceID] += b.End.Sub(b.Start).Hours()
	}
	type resStat struct {
		Resource config.Resource
		Count    int
		Hours    float64
	}
	var perResource []resStat
	for _, res := range s.cfg.Resources {
		perResource = append(perResource, resStat{Resource: res, Count: counts[res.ID], Hours: hours[res.ID]})
	}

	v.Title = i18n.T(v.Lang, "admin.title")
	v.Data = map[string]any{
		"Rows":        s.rows(v.Lang, list, loc),
		"Filter":      f,
		"Resources":   s.cfg.Resources,
		"Stats":       stats,
		"PerResource": perResource,
		"ExportURL":   "/admin/export.csv?" + r.URL.RawQuery,
		"BackURL":     backURL(r),
	}
	s.render(w, r, http.StatusOK, "admin.html", v)
}

// backURL preserves the current filter so cancelling returns to the same view.
func backURL(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return "/admin"
	}
	return "/admin?" + r.URL.RawQuery
}

func (s *Server) handleAdminCSV(w http.ResponseWriter, r *http.Request, v *view) {
	now := s.now()
	loc := s.cfg.Location()
	f := readFilter(r)
	sf := f.toStoreFilter(now, loc)
	sf.Limit = 0

	list, err := s.store.Search(r.Context(), sf)
	if err != nil {
		http.Error(w, "could not read the bookings", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		"attachment; filename=\"bokningar-"+now.In(loc).Format("2006-01-02")+".csv\"")
	// A BOM makes Excel open the UTF-8 file with the right encoding.
	w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	cw.Comma = ';'
	defer cw.Flush()
	lang := v.Lang
	cw.Write([]string{"id",
		i18n.T(lang, "csv.resource"), i18n.T(lang, "csv.start"), i18n.T(lang, "csv.end"),
		i18n.T(lang, "csv.hours"), i18n.T(lang, "csv.name"), i18n.T(lang, "csv.apartment"),
		i18n.T(lang, "csv.mattermost"), i18n.T(lang, "csv.email"), i18n.T(lang, "csv.phone"),
		i18n.T(lang, "csv.note"), i18n.T(lang, "csv.status"), i18n.T(lang, "csv.created")})
	for _, b := range list {
		res, ok := s.cfg.Resource(b.ResourceID)
		name := b.ResourceID
		if ok {
			name = res.NameFor(string(lang))
		}
		cw.Write([]string{
			b.ID, name,
			b.Start.In(loc).Format("2006-01-02 15:04"),
			b.End.In(loc).Format("2006-01-02 15:04"),
			strconv.FormatFloat(b.End.Sub(b.Start).Hours(), 'f', 2, 64),
			b.Name, b.Apartment, b.MMUsername, b.Email, b.Phone, b.Note, string(b.Status),
			b.CreatedAt.In(loc).Format("2006-01-02 15:04"),
		})
	}
}

func (s *Server) handleAdminCancel(w http.ResponseWriter, r *http.Request, v *view) {
	id := r.PathValue("id")
	b, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.errorPage(w, r, http.StatusNotFound, "error.nobooking", "")
		return
	}
	if err := s.store.Cancel(r.Context(), id, "", true, s.now()); err != nil {
		s.errorPage(w, r, http.StatusConflict, "error.nocancel", "error.nocancel.done")
		return
	}
	res, ok := s.cfg.Resource(b.ResourceID)
	if !ok {
		res = config.Resource{ID: b.ResourceID, Name: b.ResourceID}
	}
	b.Status = store.StatusCancelled
	s.log.Info("booking cancelled by admin", "id", id)
	go s.notifyCancelled(b, res)

	back := r.FormValue("back")
	if back == "" || !strings.HasPrefix(back, "/admin") {
		back = "/admin"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
