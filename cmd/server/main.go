// Command server runs the Rudbeckia booking site.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	// Embedding the timezone database lets the container run FROM scratch.
	_ "time/tzdata"

	"github.com/mikaelo/booking.rudbeckia.nu/internal/auth"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/config"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/demo"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/mattermost"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/store"
	"github.com/mikaelo/booking.rudbeckia.nu/internal/web"
)

// version is stamped at build time with -ldflags.
var version = "dev"

func main() {
	var (
		checkConfig = flag.Bool("check-config", false,
			"load and validate the configuration file, then exit")
		showVersion = flag.Bool("version", false, "print the version and exit")
		demoMode    = flag.Bool("demo", false,
			"run a throwaway demo: default passwords, example bookings, banner on every page")
	)
	flag.Parse()

	if *demoMode {
		os.Setenv("DEMO", "true")
	}

	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(os.Getenv("LOG_LEVEL")),
	}))
	slog.SetDefault(log)

	if *checkConfig {
		if err := check(log); err != nil {
			log.Error("configuration is not valid", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// check validates the configuration without needing any secrets, so it can run
// in CI and as a pre-flight step before a deploy.
func check(log *slog.Logger) error {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	active := 0
	for _, r := range cfg.Resources {
		if r.Active() {
			active++
		}
	}
	log.Info("configuration is valid",
		"path", path,
		"timezone", cfg.Site.Timezone,
		"language", cfg.Site.Language,
		"categories", len(cfg.Categories),
		"resources", len(cfg.Resources),
		"bookable", active)
	for _, r := range cfg.Resources {
		state := "bookable"
		if !r.Active() {
			state = "disabled"
		}
		log.Info("resource", "id", r.ID, "name", r.Name, "mode", r.Rules.Mode, "state", state)
	}
	return nil
}

func run(log *slog.Logger) error {
	rt, err := config.LoadRuntime()
	if err != nil {
		return err
	}
	cfg, err := config.Load(rt.ConfigPath)
	if err != nil {
		return err
	}
	log.Info("configuration loaded",
		"path", rt.ConfigPath,
		"resources", len(cfg.Resources),
		"timezone", cfg.Site.Timezone,
		"language", cfg.Site.Language)

	st, err := store.Open(rt.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if rt.Demo {
		n, err := demo.Seed(context.Background(), st, cfg, time.Now())
		if err != nil {
			return fmt.Errorf("seed demo data: %w", err)
		}
		log.Warn("DEMO MODE — do not use this for a real house",
			"password", rt.Password, "admin_password", rt.AdminPassword,
			"seeded_bookings", n, "database", rt.DBPath)
	}

	secure := strings.HasPrefix(rt.BaseURL, "https://")
	guard := auth.New(rt.Password, rt.AdminPassword, rt.SessionSecret, rt.SessionMaxAge, secure)
	bot := mattermost.New(rt.Mattermost.URL, rt.Mattermost.Token, log)
	if bot.Enabled() {
		// Check the token now: a bot that cannot log in must be a startup
		// complaint, not a mystery when the first booking is made.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		me, err := bot.Verify(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("mattermost: %w", err)
		}
		log.Info("mattermost bot ready", "server", rt.Mattermost.URL,
			"bot", me.Username, "allowed", allowedLabel(rt.Mattermost))
	} else {
		log.Warn("Mattermost is not configured; confirmations will only be logged " +
			"and members are taken as typed")
	}
	if !guard.HasAdmin() {
		log.Warn("ADMIN_PASSWORD is not set; the /admin view is unavailable")
	}

	srv, err := web.New(cfg, rt, st, guard, bot, log)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              rt.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", rt.ListenAddr, "base_url", rt.BaseURL, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Info("shutting down", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpSrv.Shutdown(ctx)
}

// allowedLabel describes the booking allow list for the startup log.
func allowedLabel(m config.MattermostSettings) string {
	if len(m.Allow) == 0 {
		return "everyone in the directory"
	}
	return m.AllowList()
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
