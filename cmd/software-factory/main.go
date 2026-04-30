package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/rberrelleza/software-factory/internal/bot"
	"github.com/rberrelleza/software-factory/internal/buildinfo"
)

func main() {
	_ = godotenv.Load()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("software-factory starting",
		"version", buildinfo.Version,
		"commit", buildinfo.Commit,
		"date", buildinfo.Date,
	)

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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := b.Run(ctx); err != nil {
		log.Error("bot stopped with error", "error", err)
		os.Exit(1)
	}
}
