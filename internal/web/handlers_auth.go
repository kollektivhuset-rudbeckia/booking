package web

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/i18n"
)

// loginThrottle slows down password guessing from a single address. It is a
// small in-memory counter, which is plenty for a house of forty people.
type loginThrottle struct {
	mu     sync.Mutex
	tries  map[string][]time.Time
	window time.Duration
	maxTry int
}

func newThrottle() *loginThrottle {
	return &loginThrottle{tries: map[string][]time.Time{}, window: 15 * time.Minute, maxTry: 10}
}

// allow reports whether another attempt from ip may be made.
func (t *loginThrottle) allow(ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cut := now.Add(-t.window)
	kept := t.tries[ip][:0]
	for _, at := range t.tries[ip] {
		if at.After(cut) {
			kept = append(kept, at)
		}
	}
	t.tries[ip] = kept
	return len(kept) < t.maxTry
}

func (t *loginThrottle) fail(ip string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tries[ip] = append(t.tries[ip], now)
}

func (t *loginThrottle) reset(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.tries, ip)
}

var throttle = newThrottle()

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	role := s.guard.Role(r)
	if role.LoggedIn() {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	v := s.newView(r, role)
	v.Title = i18n.T(v.Lang, "login.title")
	v.Data = map[string]any{"Next": r.URL.Query().Get("next")}
	s.render(w, r, http.StatusOK, "login.html", v)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	ip := s.clientIP(r)
	now := s.now()
	next := safeNext(r.FormValue("next"))

	v := s.newView(r, "")
	v.Title = i18n.T(v.Lang, "login.title")

	if !throttle.allow(ip, now) {
		v.Data = map[string]any{"Next": r.FormValue("next"),
			"Error": i18n.T(v.Lang, "login.throttled")}
		s.render(w, r, http.StatusTooManyRequests, "login.html", v)
		return
	}

	role := s.guard.Check(strings.TrimSpace(r.FormValue("password")))
	if !role.LoggedIn() {
		throttle.fail(ip, now)
		s.log.Warn("failed login", "ip", ip)
		v.Data = map[string]any{"Next": r.FormValue("next"), "Error": i18n.T(v.Lang, "login.wrong")}
		s.render(w, r, http.StatusUnauthorized, "login.html", v)
		return
	}
	throttle.reset(ip)
	s.guard.Issue(w, role)
	s.log.Info("login", "role", role, "ip", ip)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.guard.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleLanguage remembers which language to serve and returns the reader to
// the page they were on. It is a form rather than a link so that following it
// cannot be cached or prefetched into a change nobody asked for.
func (s *Server) handleLanguage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	lang, ok := i18n.Parse(r.FormValue("lang"))
	if !ok {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	i18n.SetCookie(w, lang, strings.HasPrefix(s.rt.BaseURL, "https://"))
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

// safeNext keeps redirects on this site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}
