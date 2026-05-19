package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const conversationAPIPrefix = "/api/v1/conversations"

func (b *Bot) conversationCollectionHandler(parentCtx context.Context, w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.conversationsHandler(w, r)
	case http.MethodPost:
		b.startConversationTurn(parentCtx, w, r, "")
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *Bot) conversationResourceHandler(parentCtx context.Context, w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, conversationAPIPrefix+"/")
	if rest == "" || rest == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	conversationID := parts[0]
	if !isSafeThreadID(conversationID) {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		b.serveConversationDetail(w, r, conversationID)
		return
	}

	switch parts[1] {
	case "turns":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		b.startConversationTurn(parentCtx, w, r, conversationID)
	case "events":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		b.conversationEventsHandler(w, r, conversationID)
	case "cancel":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		b.conversationCancelHandler(w, r, conversationID)
	case "attachments":
		if len(parts) != 3 || !isSafeAttachmentID(parts[2]) {
			http.NotFound(w, r)
			return
		}
		b.serveConversationAttachmentDownload(w, r, conversationID, parts[2])
	default:
		http.NotFound(w, r)
	}
}

func (b *Bot) conversationEventsHandler(w http.ResponseWriter, r *http.Request, conversationID string) {
	q := r.URL.Query()
	q.Set("session", conversationID)
	r2 := r.Clone(r.Context())
	r2.URL = cloneURL(r.URL)
	r2.URL.RawQuery = q.Encode()
	b.chatStreamHandler(w, r2)
}

func (b *Bot) conversationCancelHandler(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := requireSameOriginUnlessAPIKey(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	payload, err := json.Marshal(map[string]string{"session_id": conversationID})
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	r2 := r.Clone(r.Context())
	r2.Header = r.Header.Clone()
	r2.Header.Set("Content-Type", "application/json")
	r2.Body = io.NopCloser(bytes.NewReader(payload))
	b.chatCancelHandler(w, r2)
}

func cloneURL(u *url.URL) *url.URL {
	cp := *u
	return &cp
}
