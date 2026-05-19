package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// conversationSummary is the shape returned by GET /api/v1/conversations.
// `Title` is derived from the first user turn so the sidebar has a
// human-readable label without us needing a dedicated DB column.
type conversationSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	PRURL     string `json:"pr_url,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// conversationDetail is the shape returned by GET /api/v1/conversations/{id}.
// Turns are the public transcript surface: each user turn carries its message,
// typed response blocks, and optional attachment metadata.
//
// Branch / GitHubOwner / GitHubRepo / SandboxID / CreatorID power the
// chat-detail metadata sidebar. They're populated lazily during the
// run (the sandbox is created before Claude has a branch name; the
// PR URL only lands when Claude finishes the first turn) so any of
// them may be empty mid-conversation.
type conversationDetail struct {
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	Status      string             `json:"status"`
	PRURL       string             `json:"pr_url,omitempty"`
	Branch      string             `json:"branch,omitempty"`
	GitHubOwner string             `json:"github_owner,omitempty"`
	GitHubRepo  string             `json:"github_repo,omitempty"`
	SandboxID   string             `json:"sandbox_id,omitempty"`
	CreatorID   string             `json:"creator_id,omitempty"`
	AgentSlug   string             `json:"agent_slug,omitempty"`
	AgentName   string             `json:"agent_name,omitempty"`
	Model       string             `json:"model,omitempty"`
	TaskOptions map[string]bool    `json:"task_options,omitempty"`
	Attachments []attachmentInfo   `json:"attachments,omitempty"`
	CreatedAt   string             `json:"created_at,omitempty"`
	Turns       []conversationTurn `json:"turns,omitempty"`
	// SXSkills is the de-duplicated list of skill names sx installed
	// for the most recent turn that ran sx. Derived server-side from
	// the persisted response_blocks (rather than stored in its own
	// column) so existing conversations from before the capture
	// shipped don't need a backfill; reading it from the latest turn
	// keeps the right-hand details panel showing the current state.
	SXSkills  []string `json:"sx_skills,omitempty"`
	UpdatedAt string   `json:"updated_at"`
}

type conversationTurn struct {
	ID          string           `json:"id"`
	Index       int              `json:"index"`
	Message     string           `json:"message"`
	Blocks      []blocks.Block   `json:"blocks,omitempty"`
	Attachments []attachmentInfo `json:"attachments,omitempty"`
}

type attachmentInfo struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	TurnIndex   int    `json:"turn_index"`
	Source      string `json:"source"`
	CreatedAt   string `json:"created_at,omitempty"`
	DownloadURL string `json:"download_url"`
}

type agentSummary struct {
	Slug         string   `json:"slug"`
	DisplayName  string   `json:"display_name"`
	Description  string   `json:"description"`
	SXBot        string   `json:"sx_bot,omitempty"`
	PersonaAsset string   `json:"persona_asset,omitempty"`
	SlackAliases []string `json:"slack_aliases,omitempty"`
	Skills       []string `json:"skills,omitempty"`
	BuiltIn      bool     `json:"built_in"`
	Default      bool     `json:"default"`
}

func (b *Bot) agentsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	if r.Method == http.MethodPost {
		if !isAdmin(p) {
			http.Error(w, "admin required", http.StatusForbidden)
			return
		}
		if err := requireSameOrigin(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		var body struct {
			Slug          string   `json:"slug"`
			DisplayName   string   `json:"display_name"`
			Description   string   `json:"description"`
			SXBot         string   `json:"sx_bot"`
			PersonaAsset  string   `json:"persona_asset"`
			PersonaPrompt string   `json:"persona_prompt"`
			SlackAliases  []string `json:"slack_aliases"`
			Skills        []string `json:"skills"`
			Enabled       *bool    `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		enabled := true
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		profile, err := store.Upsert(r.Context(), p.OrgID, agents.Profile{
			Slug:          body.Slug,
			DisplayName:   body.DisplayName,
			Description:   body.Description,
			SXBot:         body.SXBot,
			PersonaAsset:  body.PersonaAsset,
			PersonaPrompt: body.PersonaPrompt,
			SlackAliases:  body.SlackAliases,
			Skills:        body.Skills,
			Enabled:       enabled,
		})
		if err != nil {
			b.log.Warn("upsert agent", "error", err, "org", p.OrgID)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, agentSummary{
			Slug:         profile.Slug,
			DisplayName:  profile.DisplayName,
			Description:  profile.Description,
			SXBot:        profile.SXBot,
			PersonaAsset: profile.PersonaAsset,
			SlackAliases: profile.SlackAliases,
			Skills:       profile.Skills,
			BuiltIn:      profile.BuiltIn,
			Default:      profile.Slug == agents.DefaultSlug,
		})
		return
	}
	profiles, err := store.List(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list agents", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]agentSummary, 0, len(profiles))
	for _, a := range profiles {
		if !a.Enabled {
			continue
		}
		out = append(out, agentSummary{
			Slug:         a.Slug,
			DisplayName:  a.DisplayName,
			Description:  a.Description,
			SXBot:        a.SXBot,
			PersonaAsset: a.PersonaAsset,
			SlackAliases: a.SlackAliases,
			Skills:       a.Skills,
			BuiltIn:      a.BuiltIn,
			Default:      a.Slug == agents.DefaultSlug,
		})
	}
	writeJSON(w, out)
}

// conversationsListLimitDefault caps a single sidebar page to 20.
// conversationsListLimitMax keeps a malicious caller from asking for
// the entire table at once. The frontend's "Load more" walks the
// pages by bumping ?offset, and conversationsListOffsetMax stops
// that walk before Postgres is asked to scan-and-skip a pathological
// number of rows (each ?offset=N is an O(N) scan ahead of LIMIT).
// conversationsListQueryMax bounds the substring search input so an
// attacker can't post a multi-megabyte ?q to make the ILIKE pattern
// matching expensive (sequential scan over conversations, twice).
const (
	conversationsListLimitDefault = 20
	conversationsListLimitMax     = 100
	conversationsListOffsetMax    = 100_000
	conversationsListQueryMax     = 256
)

func (b *Bot) conversationsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())

	q := r.URL.Query()
	limit := parseClampedInt(q.Get("limit"), conversationsListLimitDefault, 1, conversationsListLimitMax)
	offset := parseClampedInt(q.Get("offset"), 0, 0, conversationsListOffsetMax)
	// Truncate by rune so we never split a multi-byte UTF-8 codepoint
	// down the middle and feed mojibake to ILIKE. The cap is a
	// substring-search ceiling, not a meaningful query length —
	// nobody types 256 characters into a chat-title search box, but
	// a script could.
	queryStr := strings.TrimSpace(q.Get("q"))
	if runes := []rune(queryStr); len(runes) > conversationsListQueryMax {
		queryStr = string(runes[:conversationsListQueryMax])
	}

	recs, err := b.convs.Search(r.Context(), p.OrgID, convstore.SearchOptions{
		CreatorID: q.Get("user"),
		Query:     queryStr,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		b.log.Error("search conversations", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]conversationSummary, 0, len(recs))
	for _, rec := range recs {
		out = append(out, conversationSummary{
			ID:        rec.ThreadID,
			Title:     conversationTitle(rec),
			Status:    b.conversationListStatus(p.OrgID, rec.ThreadID),
			PRURL:     rec.PRURL,
			UpdatedAt: rec.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, out)
}

// parseClampedInt parses s as an integer and clamps the result to
// [min, max]. Returns def for empty / unparseable input. Used by
// pagination handlers to avoid hand-rolling the same five-line dance.
// Pass math.MaxInt for max when the caller wants no upper bound.
func parseClampedInt(s string, def, min, max int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// repositorySummary is the shape returned by GET /api/v1/repositories. The
// composer repo picker reads owner/name to build the "owner/name" label
// shown in the chip and to round-trip the selection back to the conversation API as the
// `repository` field. Default branch is surfaced so a future "branch:"
// hint can render alongside without a second fetch.
type repositorySummary struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// repositoriesListLimitDefault caps a single composer-picker page to 20.
// The frontend retrieves the first 20 immediately and lets the user
// search for repos outside that initial slice so orgs with thousands of
// repos still work without paging the whole catalogue into the browser.
const (
	repositoriesListLimitDefault = 20
	repositoriesListLimitMax     = 100
	repositoriesListQueryMax     = 128
)

func (b *Bot) repositoriesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())

	q := r.URL.Query()
	limit := parseClampedInt(q.Get("limit"), repositoriesListLimitDefault, 1, repositoriesListLimitMax)
	queryStr := strings.TrimSpace(q.Get("q"))
	if runes := []rune(queryStr); len(runes) > repositoriesListQueryMax {
		queryStr = string(runes[:repositoriesListQueryMax])
	}

	// Handlers that test against a bot built without a DB pool (the
	// bypass-bot path used by unit tests) won't have a Queries handle.
	// Return an empty page instead of NPE-ing so the picker still
	// renders the "Use org default" row cleanly.
	if b.store == nil || b.store.Queries == nil {
		writeJSON(w, []repositorySummary{})
		return
	}

	rows, err := b.store.Queries.ListGithubReposByOrg(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list repositories", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := filterRepositoriesForPicker(rows, queryStr, limit)
	writeJSON(w, out)
}

// filterRepositoriesForPicker is the case-insensitive substring filter
// + cap the composer repo dropdown uses to slice the org's repo list
// down to a single page. Pulled out as a free function so the
// substring + limit semantics can be exercised in isolation without
// standing up a Postgres fixture.
func filterRepositoriesForPicker(rows []sqlc.GithubRepo, query string, limit int) []repositorySummary {
	if limit <= 0 {
		return []repositorySummary{}
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	out := make([]repositorySummary, 0, limit)
	for _, row := range rows {
		if needle != "" {
			haystack := strings.ToLower(row.Owner + "/" + row.Name)
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		out = append(out, repositorySummary{
			Owner:         row.Owner,
			Name:          row.Name,
			DefaultBranch: row.DefaultBranch,
			Private:       row.Private,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// memberSummary is the shape returned by GET /api/v1/members.
type memberSummary struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func (b *Bot) membersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	members, err := b.auth.ListMembers(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list members", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]memberSummary, 0, len(members))
	for _, m := range members {
		// Inactive memberships (removed users) and pending ones
		// (invited but not yet accepted) can never be the creator of a
		// conversation, so they'd only appear in the dropdown to
		// produce an empty list when chosen — and surfacing former
		// teammates by name is mildly information-leaky.
		if m.Status != "active" {
			continue
		}
		out = append(out, memberSummary{
			UserID:      m.UserID,
			DisplayName: m.DisplayName(),
			Email:       m.Email,
		})
	}
	// Member lists change rarely (an admin invites or removes someone)
	// but are fetched on every initial chat-page load. ListMembers does
	// O(N) per-user GETs to WorkOS, so a short browser-side cache cuts
	// most of those round-trips for repeat navigations within the
	// 5-minute window without making membership changes feel stuck.
	w.Header().Set("Cache-Control", "private, max-age=300")
	writeJSON(w, out)
}

func (b *Bot) serveConversationDetail(w http.ResponseWriter, r *http.Request, threadID string) {
	p, _ := auth.FromContext(r.Context())

	switch r.Method {
	case http.MethodGet:
		rec, err := b.convs.Get(r.Context(), p.OrgID, threadID)
		if err != nil {
			if errors.Is(err, convstore.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			b.log.Error("get conversation", "error", err, "org", p.OrgID, "thread", threadID)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, b.conversationDetailResponse(r.Context(), p.OrgID, rec, conversationIncludesFromQuery(r.URL.Query())))

	case http.MethodDelete:
		if err := requireSameOriginUnlessAPIKey(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		// Refuse the delete if a run is currently in flight on
		// this (org, thread). Otherwise the terminal Upsert that
		// fires when the agent finishes — bot.go runFreshAgent /
		// handleFollowUp at end-of-run — would silently re-INSERT
		// the row we just dropped (UpsertConversation is a generic
		// UPSERT). Same shape as the Delete-bootstrap race we
		// already closed in applySpecImprovements via GetSpec
		// re-check; here we close it from the other side because
		// teaching every terminal Upsert site to re-fetch is more
		// invasive than a single 409 here.
		if run := b.live.Get(p.OrgID, threadID); run != nil {
			http.Error(w, "this chat has a turn in flight; wait for it to finish before deleting", http.StatusConflict)
			return
		}
		if b.runs != nil && b.runs.Enabled() {
			if active, err := b.runs.ActiveForThread(r.Context(), p.OrgID, threadID); err == nil {
				b.log.Info("conversation delete rejected due active durable run",
					"org", p.OrgID, "thread", threadID, "run_id", active.ID, "state", active.State)
				http.Error(w, "this chat has a turn in flight; wait for it to finish before deleting", http.StatusConflict)
				return
			} else if !errors.Is(err, pgx.ErrNoRows) {
				b.log.Warn("active run lookup for conversation delete", "org", p.OrgID, "thread", threadID, "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
		}
		if err := b.convs.Delete(r.Context(), p.OrgID, threadID); err != nil {
			b.log.Error("delete conversation", "error", err, "org", p.OrgID, "thread", threadID)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPatch:
		if err := requireSameOriginUnlessAPIKey(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		title := strings.TrimSpace(body.Title)
		if title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		// Cap server-side at 200 runes — the chat.html input has the same
		// maxlength, but a direct API client could otherwise persist an
		// unbounded string into the DB.
		if runes := []rune(title); len(runes) > 200 {
			http.Error(w, "title must be 200 characters or fewer", http.StatusBadRequest)
			return
		}
		if err := b.convs.Rename(r.Context(), p.OrgID, threadID, title); err != nil {
			if errors.Is(err, convstore.ErrNotFound) {
				http.Error(w, "conversation not found", http.StatusNotFound)
				return
			}
			b.log.Error("rename conversation", "error", err, "org", p.OrgID, "thread", threadID)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.Header().Set("Allow", "GET, DELETE, PATCH")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *Bot) resolveAgent(ctx context.Context, orgID, slug string) (resolvedSlug, name string) {
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	resolvedSlug = slug
	if slug != "" {
		if agent, err := store.GetBySlug(ctx, orgID, slug); err == nil {
			resolvedSlug = agent.Slug
			name = agent.DisplayName
		}
	}
	return
}

type conversationIncludeOptions struct {
	Turns       bool
	Attachments bool
}

func conversationIncludesFromQuery(q url.Values) conversationIncludeOptions {
	values := q["include"]
	if len(values) == 0 {
		return conversationIncludeOptions{Turns: true}
	}
	opts := conversationIncludeOptions{}
	for _, raw := range values {
		for part := range strings.SplitSeq(raw, ",") {
			switch strings.TrimSpace(strings.ToLower(part)) {
			case "all":
				opts.Turns = true
				opts.Attachments = true
			case "turns":
				opts.Turns = true
			case "attachments":
				opts.Attachments = true
			}
		}
	}
	return opts
}

func (b *Bot) conversationDetailResponse(ctx context.Context, orgID string, rec convstore.Record, include conversationIncludeOptions) conversationDetail {
	var createdAt string
	if !rec.CreatedAt.IsZero() {
		createdAt = rec.CreatedAt.UTC().Format(time.RFC3339)
	}
	agentSlug, agentName := b.resolveAgent(ctx, orgID, rec.AgentSlug)
	var attachments []attachmentInfo
	if include.Attachments {
		attachments = b.attachmentInfos(ctx, orgID, rec.ThreadID)
	}
	detail := conversationDetail{
		ID:          rec.ThreadID,
		Title:       conversationTitle(rec),
		Status:      b.conversationStatus(ctx, orgID, rec.ThreadID),
		PRURL:       rec.PRURL,
		Branch:      rec.Branch,
		GitHubOwner: rec.GitHubOwner,
		GitHubRepo:  rec.GitHubRepo,
		SandboxID:   rec.SandboxID,
		CreatorID:   rec.CreatorID,
		AgentSlug:   agentSlug,
		AgentName:   agentName,
		Model:       string(normalizeClaudeModel(ClaudeModel(rec.Model))),
		TaskOptions: rec.TaskOptions,
		Attachments: attachments,
		CreatedAt:   createdAt,
		SXSkills:    extractSXSkills(rec.ResponseBlocks),
		UpdatedAt:   rec.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if include.Turns {
		detail.Turns = conversationTurns(rec, attachments, include.Attachments)
	}
	return detail
}

func conversationTurns(rec convstore.Record, attachments []attachmentInfo, includeAttachments bool) []conversationTurn {
	if len(rec.History) == 0 {
		return nil
	}
	attachmentsByTurn := map[int][]attachmentInfo{}
	if includeAttachments {
		for _, a := range attachments {
			attachmentsByTurn[a.TurnIndex] = append(attachmentsByTurn[a.TurnIndex], a)
		}
	}
	turns := make([]conversationTurn, 0, len(rec.History))
	for i, message := range rec.History {
		var blocksForTurn []blocks.Block
		if i < len(rec.ResponseBlocks) {
			blocksForTurn = rec.ResponseBlocks[i]
		}
		turn := conversationTurn{
			ID:      conversationTurnID(rec.ThreadID, i, message),
			Index:   i,
			Message: message,
			Blocks:  blocksForTurn,
		}
		if includeAttachments {
			turn.Attachments = attachmentsByTurn[i]
		}
		turns = append(turns, turn)
	}
	return turns
}

func conversationTurnID(threadID string, index int, message string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(threadID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.Itoa(index)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(message))
	return "turn_" + hex.EncodeToString(h.Sum(nil)[:12])
}

func (b *Bot) conversationListStatus(orgID, threadID string) string {
	if b.live != nil && b.live.Get(orgID, threadID) != nil {
		return "running"
	}
	return "idle"
}

func (b *Bot) conversationStatus(ctx context.Context, orgID, threadID string) string {
	if b.live != nil && b.live.Get(orgID, threadID) != nil {
		return "running"
	}
	if b.runs != nil && b.runs.Enabled() {
		latest, err := b.runs.LatestForThread(ctx, orgID, threadID)
		if err == nil {
			return latest.State
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			b.log.Warn("latest run lookup for conversation status", "org", orgID, "thread", threadID, "error", err)
		}
	}
	return "idle"
}

func (b *Bot) serveConversationAttachmentDownload(w http.ResponseWriter, r *http.Request, threadID, attachmentID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	attachment, err := b.convs.GetAttachment(r.Context(), p.OrgID, attachmentID)
	if err != nil {
		if errors.Is(err, convstore.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		b.log.Error("get conversation attachment", "error", err, "org", p.OrgID, "attachment", attachmentID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if threadID != "" && attachment.ThreadID != threadID {
		http.NotFound(w, r)
		return
	}
	contentType := normalizeAttachmentContentType(attachment.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(attachment.Data)), 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": attachment.Filename,
	}))
	_, _ = w.Write(attachment.Data)
}

func (b *Bot) attachmentInfos(ctx context.Context, orgID, threadID string) []attachmentInfo {
	attachments, err := b.convs.ListAttachments(ctx, orgID, threadID)
	if err != nil {
		b.log.Warn("list conversation attachments", "org", orgID, "thread", threadID, "error", err)
		return nil
	}
	out := make([]attachmentInfo, 0, len(attachments))
	for _, a := range attachments {
		var createdAt string
		if !a.CreatedAt.IsZero() {
			createdAt = a.CreatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, attachmentInfo{
			ID:          a.ID,
			Filename:    a.Filename,
			ContentType: a.ContentType,
			SizeBytes:   a.SizeBytes,
			TurnIndex:   a.TurnIndex,
			Source:      a.Source,
			CreatedAt:   createdAt,
			DownloadURL: "/api/v1/conversations/" + url.PathEscape(threadID) + "/attachments/" + url.PathEscape(a.ID),
		})
	}
	return out
}

// conversationTitle derives a sidebar label. If the user has set a custom
// title it is returned as-is. Otherwise the label is derived from the first
// user turn, trimmed and capped. Falls back to a generic placeholder so a
// record with empty history still renders something selectable. Truncation
// is rune-aware so non-ASCII prompts don't get split mid-codepoint, and
// CR/LF/CRLF are normalized to spaces so a multi-line first prompt renders
// as a single sidebar line.
func conversationTitle(rec convstore.Record) string {
	if rec.CustomTitle != "" {
		return rec.CustomTitle
	}
	if len(rec.History) == 0 {
		return "New chat"
	}
	first := strings.TrimSpace(rec.History[0])
	if first == "" {
		return "New chat"
	}
	const maxRunes = 80
	if runes := []rune(first); len(runes) > maxRunes {
		first = string(runes[:maxRunes]) + "…"
	}
	first = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(first)
	return first
}

// extractSXSkills walks the persisted response_blocks for the most
// recent turn that carries an sx-skills capture block and returns the
// list. Walking newest-first matters: a follow-up that re-runs sx
// install may install a different set than the original turn, and the
// right-hand details panel should reflect the freshest snapshot. The
// payload was written by agentLineRouter.emitSXSkills as a
// KindNotify block whose Meta carries the skill names under
// SXSkillsMetaKey; if neither the block nor a parseable list is
// present we return nil so the UI shows nothing rather than an empty
// "Skills" row.
func extractSXSkills(turns [][]blocks.Block) []string {
	for i := len(turns) - 1; i >= 0; i-- {
		turn := turns[i]
		for j := len(turn) - 1; j >= 0; j-- {
			block := turn[j]
			if block.Kind != blocks.KindNotify || block.Meta == nil {
				continue
			}
			raw, ok := block.Meta[SXSkillsMetaKey]
			if !ok {
				continue
			}
			skills, ok := coerceStringSlice(raw)
			if !ok {
				continue
			}
			return skills
		}
	}
	return nil
}

// coerceStringSlice accepts either a []string or a []any (the JSON
// round-trip through JSONB hands back []any even when the writer
// stored a []string) and returns a clean []string. Returns ok=false
// when the value is neither shape — callers fall back to "no skills"
// so a malformed block can't surface bad data in the UI.
func coerceStringSlice(v any) ([]string, bool) {
	switch s := v.(type) {
	case []string:
		out := make([]string, 0, len(s))
		for _, name := range s {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
		return out, true
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if name, ok := item.(string); ok {
				if name = strings.TrimSpace(name); name != "" {
					out = append(out, name)
				}
			}
		}
		return out, true
	}
	return nil, false
}

// isSafeThreadID guards path segments used to look up conversations.
// Browser-generated thread ids are UUIDs (hex + hyphens); Slack thread
// timestamps look like "1700000000.123456". Both are covered by this
// conservative charset.
func isSafeThreadID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func isSafeAttachmentID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
