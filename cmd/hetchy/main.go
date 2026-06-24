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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"

	"github.com/sleuth-io/hetchy/db/migrations"
	"github.com/sleuth-io/hetchy/internal/bot"
	"github.com/sleuth-io/hetchy/internal/buildinfo"
)

// parseLogLevel maps LOG_LEVEL to slog.Level. Unset or unrecognized
// values fall back to Info — same as the original hardcoded default.
// Setting LOG_LEVEL=debug locally surfaces the per-line sandbox stdout
// /stderr logs that exec.go emits at Debug, so `make bot` (which now
// always mirrors to LOG_FILE) shows what the agent is doing inside the
// container in real time.
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
	backfillPRStates := flag.Bool("backfill-pr-states", false, "Refresh stored GitHub pull request state for conversations with PR URLs and exit")
	backfillPRStatesLimit := flag.Int("backfill-pr-states-limit", 1000, "Maximum conversations to scan when backfilling PR state")
	backfillPRStatesForce := flag.Bool("backfill-pr-states-force", false, "Refresh PR state even for conversations checked before")
	dispatchDueJobs := flag.Bool("dispatch-due-jobs", false, "Claim due scheduled jobs, run them, and exit")
	jobDispatchLimit := flag.Int("job-dispatch-limit", 100, "Maximum due jobs to claim in one dispatcher invocation")
	jobDispatchConcurrency := flag.Int("job-dispatch-concurrency", 100, "Maximum scheduled jobs to run concurrently")
	flag.Parse()

	_ = godotenv.Load()

	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	// Railway (and most log aggregators) treat stderr as error-level regardless of
	// the message's actual level. JSON on stdout lets Railway parse the "level"
	// field and display each record at the correct severity.
	//
	// Exception: one-shot subcommands write machine-readable text to stdout
	// (e.g. "schema version: N"), so their logs go to stderr to keep stdout clean
	// for callers like `make db-up` that parse that output.
	logDest := os.Stdout
	oneShot := *migrateFlag || *migrateDown >= 0 || *migrateStatus || *backfillPRStates || *dispatchDueJobs
	if oneShot {
		logDest = os.Stderr
	}
	log := slog.New(slog.NewJSONHandler(logDest, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	log.Info("hetchy starting",
		"version", buildinfo.Version,
		"commit", buildinfo.Commit,
		"date", buildinfo.Date,
		"sandbox_snapshot_version", buildinfo.SandboxSnapshotVersion,
		"log_level", level.String(),
	)

	if *migrateFlag || *migrateDown >= 0 || *migrateStatus {
		runMigrate(log, *migrateFlag, *migrateDown, *migrateStatus)
		return
	}
	if *backfillPRStates {
		runBackfillPRStates(log, *backfillPRStatesLimit, *backfillPRStatesForce)
		return
	}
	if *dispatchDueJobs {
		runDispatchDueJobs(log, *jobDispatchLimit, *jobDispatchConcurrency)
		return
	}

	cfg, err := bot.LoadConfig()
	if err != nil {
		log.Error("config load failed", "error", err)
		os.Exit(1)
	}
	if cfg.AuthBypass {
		log.Warn("auth bypass mode enabled — WorkOS authentication is disabled")
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

func runDispatchDueJobs(log *slog.Logger, limit, concurrency int) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Error("DATABASE_URL is required for job dispatch")
		os.Exit(1)
	}
	ready, err := jobDispatchSchemaReady(context.Background(), log, databaseURL)
	if err != nil {
		log.Error("job dispatch schema check failed", "error", err)
		os.Exit(1)
	}
	if !ready {
		fmt.Println("job dispatch: skipped schema_not_ready")
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

	result, err := b.DispatchDueJobs(ctx, bot.JobDispatchOptions{
		Limit:       int32(limit),
		Concurrency: concurrency,
	})
	if err != nil {
		log.Error("job dispatch failed", "error", err)
		os.Exit(1)
	}
	fmt.Printf("job dispatch: claimed=%d started=%d succeeded=%d failed=%d\n",
		result.Claimed, result.Started, result.Succeeded, result.Failed)
}

func jobDispatchSchemaReady(ctx context.Context, log *slog.Logger, databaseURL string) (bool, error) {
	if err := waitForDB(ctx, log, databaseURL); err != nil {
		return false, err
	}
	expected, err := migrations.ExpectedVersion()
	if err != nil {
		return false, err
	}
	current, dirty, err := migrations.Version(databaseURL)
	if err != nil {
		return false, err
	}
	if dirty {
		return false, fmt.Errorf("database schema is dirty at version %d", current)
	}
	if !jobDispatchSchemaMatches(current, expected) {
		log.Warn("database schema does not match dispatcher binary, skipping job dispatch",
			"database_version", current,
			"binary_schema_version", expected,
		)
		return false, nil
	}
	return true, nil
}

func jobDispatchSchemaMatches(current, expected uint) bool {
	return current == expected
}

func runBackfillPRStates(log *slog.Logger, limit int, force bool) {
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

	result, err := b.BackfillConversationPRStates(ctx, limit, force)
	if err != nil {
		log.Error("backfill PR states failed", "error", err)
		os.Exit(1)
	}
	fmt.Printf("pr state backfill: scanned=%d updated=%d skipped=%d failed=%d\n",
		result.Scanned, result.Updated, result.Skipped, result.Failed)
}

// waitForDB retries a TCP ping against databaseURL until it succeeds or the
// deadline is exceeded. It is used by the migration path so the one-shot
// container survives a slow Postgres start without an immediate failure.
func waitForDB(ctx context.Context, log *slog.Logger, databaseURL string) error {
	const (
		maxWait     = 60 * time.Second
		initialWait = 2 * time.Second
		maxDelay    = 16 * time.Second
	)
	ctx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()

	delay := initialWait
	var lastErr error
	for attempt := 1; ; attempt++ {
		conn, err := pgx.Connect(ctx, databaseURL)
		if err == nil {
			_ = conn.Close(ctx)
			return nil
		}
		lastErr = err
		log.Warn("database not ready, retrying", "attempt", attempt, "delay", delay, "error", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("database did not become ready within %s: %w", maxWait, lastErr)
		case <-time.After(delay):
		}
		delay = min(delay*2, maxDelay)
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

	// --migrate-down is a manual recovery operation; skipping waitForDB lets
	// the operator see the connection error immediately rather than waiting 60s.
	if up || status {
		if err := waitForDB(context.Background(), log, url); err != nil {
			log.Error("database unavailable", "error", err)
			os.Exit(1)
		}
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
