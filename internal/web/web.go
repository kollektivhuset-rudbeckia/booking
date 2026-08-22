// Package web serves the booking site: server-rendered HTML, no build step,
// no client-side framework.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/auth"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/mattermost"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

// The patterns are by extension rather than the whole directory, so the
// browser-side test file next to members.js stays out of the binary.
//
//go:embed static/*.css static/*.js static/*.png
var staticFS embed.FS

// Server holds everything the handlers need.
type Server struct {
	cfg   *config.Config
	rt    config.Runtime
	store *store.Store
	guard *auth.Guard
	mm    *mattermost.Client
	log   *slog.Logger
	// tpl holds one parsed set per language. The language is baked into the
	// template functions, so a page can say {{t "key"}} and get the right
	// words without every call site passing a language around.
	tpl map[i18n.Lang]map[string]*template.Template
	now func() time.Time

	// members caches the bookable directory the user picker searches.
	members memberCache

	// assets maps a static file to a short content hash, so a new release is
	// fetched instead of served from a browser cache for another hour.
	assets map[string]string
}

// pages are the top-level templates. Each is parsed into its own set together
// with the shared layout, because every page defines a "content" block and Go
// templates share one namespace per set.
var pages = []string{
	"index.html", "login.html", "error.html", "booking.html", "mine.html",
	"admin.html", "resource_hours.html", "resource_days.html", "upcoming.html",
}

// layouts are included in every page set.
var layouts = []string{"base.html", "fields.html"}

// New builds the HTTP server.
func New(cfg *config.Config, rt config.Runtime, st *store.Store, guard *auth.Guard, mm *mattermost.Client, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, rt: rt, store: st, guard: guard, mm: mm, log: log, now: time.Now}
	var err error
	if s.assets, err = hashAssets(); err != nil {
		return nil, err
	}
	s.tpl = make(map[i18n.Lang]map[string]*template.Template, len(i18n.Langs))
	for _, lang := range i18n.Langs {
		set := make(map[string]*template.Template, len(pages))
		for _, page := range pages {
			files := make([]string, 0, len(layouts)+1)
			for _, l := range layouts {
				files = append(files, "templates/"+l)
			}
			files = append(files, "templates/"+page)
			t, err := template.New(page).Funcs(s.funcs(lang)).ParseFS(templateFS, files...)
			if err != nil {
				return nil, fmt.Errorf("parse template %s (%s): %w", page, lang, err)
			}
			set[page] = t
		}
		s.tpl[lang] = set
	}
	return s, nil
}

// hashAssets fingerprints the embedded static files. The files live in the
// binary, so this happens once at startup and cannot change afterwards.
func hashAssets() (map[string]string, error) {
	out := map[string]string{}
	entries, err := fs.ReadDir(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("read static files: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := staticFS.ReadFile("static/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read static/%s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		out[e.Name()] = hex.EncodeToString(sum[:])[:10]
	}
	return out, nil
}

// asset returns the URL for a static file, stamped with its content hash.
// Browsers may then cache it for as long as they like: a changed file has a
// changed address, so nobody is left running last week's JavaScript.
func (s *Server) asset(name string) string {
	if sum, ok := s.assets[name]; ok {
		return "/static/" + name + "?v=" + sum
	}
	return "/static/" + name
}

// defaultLang is the language a visitor gets before choosing one, and the one
// anything written to somebody who is not reading a page right now goes out in.
func (s *Server) defaultLang() i18n.Lang {
	lang, _ := i18n.Parse(s.cfg.Site.Language)
	return lang
}

// lang is the language for this request: the reader's own choice, then their
// browser's preference, then the deployment's.
func (s *Server) lang(r *http.Request) i18n.Lang {
	return i18n.FromRequest(r, s.defaultLang())
}

// Handler returns the router with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(fileServer)))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("POST /sprak", s.handleLanguage)

	mux.Handle("GET /{$}", s.member(s.handleIndex))
	mux.Handle("GET /resurs/{id}", s.member(s.handleResource))
	mux.Handle("GET /resurs/{id}/bokningar", s.member(s.handleResourceUpcoming))
	mux.Handle("POST /resurs/{id}/boka", s.member(s.handleCreateBooking))
	mux.Handle("GET /bokning/{id}", s.member(s.handleBooking))
	mux.Handle("GET /bokning/{id}/kalender.ics", s.member(s.handleBookingICS))
	mux.Handle("POST /bokning/{id}/avboka", s.member(s.handleCancel))
	mux.Handle("GET /mina", s.member(s.handleMyBookings))
	mux.Handle("GET /medlemmar", s.member(s.handleMemberSearch))
	mux.Handle("GET /kalender/{id}/flode.ics", s.member(s.handleResourceFeed))

	mux.Handle("GET /admin", s.admin(s.handleAdmin))
	mux.Handle("GET /admin/export.csv", s.admin(s.handleAdminCSV))
	mux.Handle("POST /admin/avboka/{id}", s.admin(s.handleAdminCancel))

	return s.recoverPanic(securityHeaders(mux))
}

// ctxKey namespaces the values the middleware puts on the request context.
type ctxKey string

const (
	ctxRole  ctxKey = "role"
	ctxIdent ctxKey = "ident"
)

// member wraps a handler so only logged-in members reach it.
func (s *Server) member(h func(http.ResponseWriter, *http.Request, *view)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := s.guard.Role(r)
		if !role.LoggedIn() {
			next := r.URL.RequestURI()
			http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
			return
		}
		h(w, r, s.newView(r, role))
	})
}

// admin wraps a handler so only the admin password reaches it.
func (s *Server) admin(h func(http.ResponseWriter, *http.Request, *view)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := s.guard.Role(r)
		if !role.LoggedIn() {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if !role.Admin() {
			s.errorPage(w, r, http.StatusForbidden, "error.adminonly", "error.adminonly.how")
			return
		}
		h(w, r, s.newView(r, role))
	})
}

// view is the data every page shares.
type view struct {
	Site config.Site
	// Lang is the language this page is rendered in, Other the one the switch
	// in the top bar moves to, and Here the address to come back to after
	// switching.
	Lang      i18n.Lang
	Other     i18n.Lang
	Here      string
	Role      auth.Role
	Ident     auth.Identity
	Now       time.Time
	Loc       *time.Location
	Path      string
	Title     string
	HasAdmin  bool
	BotOn     bool
	AllowList string
	Demo      bool
	DemoPass  string
	DemoAdmin string
	Flash     string
	FlashKind string
	Data      any
}

func (s *Server) newView(r *http.Request, role auth.Role) *view {
	lang := s.lang(r)
	return &view{
		Site:      s.cfg.Site,
		Lang:      lang,
		Other:     lang.Other(),
		Here:      r.URL.RequestURI(),
		Role:      role,
		Ident:     s.guard.Identity(r),
		Now:       s.now().In(s.cfg.Location()),
		Loc:       s.cfg.Location(),
		Path:      r.URL.Path,
		HasAdmin:  s.guard.HasAdmin(),
		BotOn:     s.mm.Enabled(),
		AllowList: s.rt.Mattermost.AllowList(),

		Demo:      s.rt.Demo,
		DemoPass:  s.rt.Password,
		DemoAdmin: s.rt.AdminPassword,
	}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, v *view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	set, ok := s.tpl[v.Lang]
	if !ok {
		set = s.tpl[i18n.Default]
	}
	t, ok := set[name]
	if !ok {
		s.log.Error("unknown template", "template", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Render into a buffer so a mid-template failure cannot emit half a page.
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, v); err != nil {
		s.log.Error("render template", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// errorPage shows one of the catalogue's error pages. Taking keys rather than
// sentences is what keeps the last few Swedish strings out of the handlers.
func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, status int, headlineKey, detailKey string) {
	lang := s.lang(r)
	detail := ""
	if detailKey != "" {
		detail = i18n.T(lang, detailKey)
	}
	s.renderError(w, r, status, i18n.T(lang, headlineKey), detail)
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, headline, detail string) {
	v := s.newView(r, s.guard.Role(r))
	v.Title = headline
	v.Data = map[string]any{"Headline": headline, "Detail": detail, "Status": status}
	s.render(w, r, status, "error.html", v)
}

// funcs builds the template helpers for one language. Every helper that says
// anything in words closes over the language, so the templates never pass one.
func (s *Server) funcs(lang i18n.Lang) template.FuncMap {
	l := string(lang)
	return template.FuncMap{
		"t":               func(key string, args ...any) string { return i18n.T(lang, key, args...) },
		"weekday":         func(t time.Time) string { return i18n.Weekday(lang, t) },
		"weekdayShort":    func(t time.Time) string { return i18n.WeekdayShort(lang, t) },
		"weekdayInitials": func() []string { return i18n.WeekdayInitials(lang) },
		"month":           func(t time.Time) string { return i18n.Month(lang, t) },
		"monthYear":       func(t time.Time) string { return i18n.MonthYear(lang, t) },
		"dateLong":        func(t time.Time) string { return i18n.DateLong(lang, t) },
		"dateLongYear":    func(t time.Time) string { return i18n.DateLongYear(lang, t) },
		"dateShort":       func(t time.Time) string { return i18n.DateShort(lang, t) },
		"interval":        func(start, end time.Time) string { return i18n.Interval(lang, start, end) },
		"relativeDay":     func(t, now time.Time) string { return i18n.RelativeDay(lang, t, now) },
		"count":           func(unit string, n int) string { return i18n.Count(lang, unit, n) },
		"plural":          func(unit string, n int) string { return i18n.Plural(lang, unit, n) },
		"nights":          func(n int) string { return i18n.Count(lang, "night", n) },
		"clock":           i18n.Clock,
		"isoDate":         i18n.ISODate,
		"titleCase":       i18n.TitleCase,
		"duration":        i18n.Duration,
		// The house's own words about a thing, in this language.
		"resName":         func(r config.Resource) string { return r.NameFor(l) },
		"resDescription":  func(r config.Resource) string { return r.DescriptionFor(l) },
		"resLocation":     func(r config.Resource) string { return r.LocationFor(l) },
		"resInstructions": func(r config.Resource) string { return r.InstructionsFor(l) },
		"catName":         func(c config.Category) string { return c.NameFor(l) },
		"catDescription":  func(c config.Category) string { return c.DescriptionFor(l) },
		"catLinkLabel":    func(c config.Category) string { return c.LinkLabelFor(l) },
		"siteTagline":     func(site config.Site) string { return site.TaglineFor(l) },
		"siteFooter":      func(site config.Site) string { return site.FooterFor(l) },
		"local": func(t time.Time) time.Time {
			return t.In(s.cfg.Location())
		},
		"pct": func(f float64) template.CSS {
			return template.CSS(fmt.Sprintf("%.4f%%", f))
		},
		"add":  func(a, b int) int { return a + b },
		"dict": dict,
		"query": func(base string, pairs ...string) template.URL {
			q := url.Values{}
			for i := 0; i+1 < len(pairs); i += 2 {
				if pairs[i+1] != "" {
					q.Set(pairs[i], pairs[i+1])
				}
			}
			if len(q) == 0 {
				return template.URL(base)
			}
			return template.URL(base + "?" + q.Encode())
		},
		"hasPrefix": strings.HasPrefix,
		"asset":     s.asset,
	}
}

func dict(pairs ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			continue
		}
		m[key] = pairs[i+1]
	}
	return m
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		// Everything is served from this origin; no external scripts or styles.
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

// cacheStatic lets browsers keep static files. A request carrying a ?v= hash
// is answered as immutable, because that exact content will never change; a
// bare request (an old page, or a hand-typed URL) gets a short cache instead,
// so a stale copy cannot outlive the day.
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic serving request", "path", r.URL.Path, "err", rec)
				s.errorPage(w, r, http.StatusInternalServerError, "error.wentwrong", "error.wentwrong.how")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the caller's address, honouring X-Forwarded-For when the
// deployment sits behind a reverse proxy.
func (s *Server) clientIP(r *http.Request) string {
	if s.rt.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}
