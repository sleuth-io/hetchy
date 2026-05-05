package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/hetchyhq/hetchy/db/migrations"
	"github.com/hetchyhq/hetchy/internal/bot"
	"github.com/hetchyhq/hetchy/internal/buildinfo"
)

// parseLogLevel maps LOG_LEVEL to slog.Level. Unset or unrecognized
// values fall back to Info — same as the original hardcoded default.
// Setting LOG_LEVEL=debug locally surfaces the per-line sandbox stdout
// /stderr logs that exec.go emits at Debug, so `make bot-with-logs`
// shows what the agent is doing inside the container in real time.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	migrateFlag := flag.Bool("migrate", false, "Apply all pending database migrations and exit")
	migrateDown := flag.Int("migrate-down", -1, "Roll back N migrations and exit (0 means roll back everything)")
	migrateStatus := flag.Bool("migrate-status", false, "Print the current schema version and exit")
	flag.Parse()

	_ = godotenv.Load()

	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	log.Info("hetchy starting",
		"version", buildinfo.Version,
		"commit", buildinfo.Commit,
		"date", buildinfo.Date,
		"log_level", level.String(),
	)

	if *migrateFlag || *migrateDown >= 0 || *migrateStatus {
		runMigrate(log, *migrateFlag, *migrateDown, *migrateStatus)
		return
	}

	cfg, err := bot.LoadConfig()
	if err != nil {
		log.Error("config load failed", "error", err)
		os.Exit(1)
	}

	b, err := bot.New(cfg, log)
	if err != nil {
		log.Error("bot init failed", "error", err)
		os.Exit(1)
	}
	defer b.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := b.Run(ctx); err != nil {
		log.Error("bot stopped with error", "error", err)
		os.Exit(1)
	}
}

// runMigrate handles --migrate / --migrate-down / --migrate-status without
// pulling in the rest of the bot's config (which requires WorkOS keys etc.).
// Only DATABASE_URL is needed.
func runMigrate(log *slog.Logger, up bool, down int, status bool) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Error("DATABASE_URL is required for migrations")
		os.Exit(1)
	}

	switch {
	case status:
		v, dirty, err := migrations.Version(url)
		if err != nil {
			log.Error("migrate status failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("schema version: %d (dirty=%t)\n", v, dirty)
	case down >= 0:
		if err := migrations.Down(url, down); err != nil {
			log.Error("migrate down failed", "error", err)
			os.Exit(1)
		}
		log.Info("migrations rolled back", "n", down)
	case up:
		if err := migrations.Up(url); err != nil {
			log.Error("migrate up failed", "error", err)
			os.Exit(1)
		}
		log.Info("migrations applied")
	}
}
