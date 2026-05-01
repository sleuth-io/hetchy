package bot

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed chat.html
var chatHTML []byte

func (b *Bot) runWeb(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", b.indexHandler)
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		b.chatHandler(ctx, w, r)
	})

	addr := ":" + b.cfg.WebPort
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	b.log.Info("web ui listening", "addr", "http://localhost"+addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("web server: %w", err)
	}
	return nil
}

func (b *Bot) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(chatHTML)
}

func (b *Bot) chatHandler(parentCtx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Text      string `json:"text"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	requestID := strconv.FormatInt(time.Now().UnixMilli(), 10)
	// If no session ID provided, treat each request as a new conversation.
	if sessionID == "" {
		sessionID = requestID
	}

	updates := make(chan string, 8)

	// Run the agent in a goroutine bound to the bot's lifetime, not the
	// request's, so a closed browser doesn't kill an in-flight build.
	go func() {
		defer close(updates)
		b.HandleRequest(parentCtx, text, requestID, sessionID, func(msg string) {
			select {
			case updates <- msg:
			case <-parentCtx.Done():
			}
		})
	}()

	for {
		select {
		case msg, ok := <-updates:
			if !ok {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				b.log.Error("json marshal failed", "error", err)
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			// Client disconnected. Drain remaining updates so the worker
			// goroutine can finish its work without blocking on the channel.
			go func() {
				for range updates {
				}
			}()
			return
		}
	}
}
