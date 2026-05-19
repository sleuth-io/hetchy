package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// repoSecretsHandler handles per-repo secret CRUD over JSON. The route
// is org-scoped (RequireOrg middleware) so we resolve installation_id
// + repo_id from a (owner, name) pair the client sends, the same way
// chat does.
//
// Wire format intentionally hides the encrypted bytes: GET returns
// only key names + a "filled" boolean, never the value. PUT accepts
// {value: ""} to clear a value while keeping the placeholder row.
//
// Routes:
//
//	GET  /api/v1/repo-secrets?owner=X&name=Y[&path=Z]
//	PUT  /api/v1/repo-secrets   body {owner, name, path, secret_name, value}
//	DELETE /api/v1/repo-secrets?owner=X&name=Y&secret_name=K[&path=Z]
type repoSecretEntry struct {
	Name   string `json:"name"`
	Filled bool   `json:"filled"`
}

type repoSecretsListResponse struct {
	Owner   string            `json:"owner"`
	Repo    string            `json:"repo"`
	Path    string            `json:"path"`
	Secrets []repoSecretEntry `json:"secrets"`
}

type repoSecretSetRequest struct {
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	SecretName string `json:"secret_name"`
	Value      string `json:"value"`
}

func (b *Bot) repoSecretsHandler(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		b.repoSecretsList(w, r, p)
	case http.MethodPut, http.MethodPost:
		b.repoSecretsSet(w, r, p)
	case http.MethodDelete:
		b.repoSecretsDelete(w, r, p)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *Bot) repoSecretsList(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if owner == "" || name == "" {
		http.Error(w, "owner and name required", http.StatusBadRequest)
		return
	}

	repo, err := b.lookupRepo(r, p.OrgID, owner, name)
	if err != nil {
		writeRepoErr(w, err)
		return
	}

	summaries, err := b.bootstrap.ListSecrets(r.Context(), repo.InstallationID, repo.RepoID, path)
	if err != nil {
		http.Error(w, "list secrets: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := repoSecretsListResponse{
		Owner: owner, Repo: name, Path: path,
		Secrets: make([]repoSecretEntry, 0, len(summaries)),
	}
	for _, sum := range summaries {
		resp.Secrets = append(resp.Secrets, repoSecretEntry{
			Name:   sum.Name,
			Filled: sum.Filled,
		})
	}
	writeJSON(w, resp)
}

func (b *Bot) repoSecretsSet(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req repoSecretSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Owner == "" || req.Name == "" || req.SecretName == "" {
		http.Error(w, "owner, name, secret_name required", http.StatusBadRequest)
		return
	}
	repo, err := b.lookupRepo(r, p.OrgID, req.Owner, req.Name)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	if err := b.bootstrap.SetSecret(r.Context(), repo.InstallationID, repo.RepoID, req.Path, req.SecretName, req.Value); err != nil {
		http.Error(w, "save secret: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("repo secret set",
		"org", p.OrgID, "actor", p.UserID,
		"owner", req.Owner, "repo", req.Name, "path", req.Path,
		"secret", req.SecretName, "cleared", req.Value == "")
	writeJSON(w, map[string]string{"status": "ok"})
}

func (b *Bot) repoSecretsDelete(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	secretName := strings.TrimSpace(r.URL.Query().Get("secret_name"))
	if owner == "" || name == "" || secretName == "" {
		http.Error(w, "owner, name, secret_name required", http.StatusBadRequest)
		return
	}
	repo, err := b.lookupRepo(r, p.OrgID, owner, name)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	if err := b.bootstrap.DeleteSecret(r.Context(), repo.InstallationID, repo.RepoID, path, secretName); err != nil {
		http.Error(w, "delete: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("repo secret deleted",
		"org", p.OrgID, "actor", p.UserID,
		"owner", owner, "repo", name, "path", path, "secret", secretName)
	writeJSON(w, map[string]string{"status": "ok"})
}

// lookupRepo locates a (owner, name) inside this org's GitHub App
// installations. Distinct from Bot.resolveRepo, which mints a fresh
// installation token for the chat agent — secrets CRUD only needs the
// row identity.
func (b *Bot) lookupRepo(r *http.Request, orgID, owner, name string) (sqlc.GithubRepo, error) {
	return b.lookupRepoForOrg(r.Context(), orgID, owner, name)
}

func (b *Bot) lookupRepoForOrg(ctx context.Context, orgID, owner, name string) (sqlc.GithubRepo, error) {
	if b.lookupRepoFn != nil {
		return b.lookupRepoFn(ctx, orgID, owner, name)
	}
	return b.store.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID,
		Owner: owner,
		Name:  name,
	})
}

// repoBootstrapResetHandler deletes the saved bootstrap spec for one
// repo so the next task on that repo runs the bootstrap loop from
// scratch. The intended use is "we shipped prompt or detect changes
// and want this repo to pick them up" — there's no UI today for
// editing a saved spec in place, so resetting + re-running on the
// next task is the cheapest way to refresh.
//
// DELETE /api/v1/repo-bootstrap?owner=X&name=Y[&path=Z]
//
// Per-secret values are deliberately preserved — they cost the user
// time to enter and the new bootstrap will declare the same set
// (modulo prompt drift). If the new spec genuinely needs a different
// secret name, the user fills it in via the existing secrets UI.
func (b *Bot) repoBootstrapResetHandler(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if owner == "" || name == "" {
		http.Error(w, "owner and name required", http.StatusBadRequest)
		return
	}
	repo, err := b.lookupRepo(r, p.OrgID, owner, name)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	if err := b.bootstrap.DeleteSpec(r.Context(), repo.InstallationID, repo.RepoID, path); err != nil {
		http.Error(w, "delete bootstrap spec: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("bootstrap spec reset",
		"org", p.OrgID, "actor", p.UserID,
		"owner", owner, "repo", name, "path", path)
	writeJSON(w, map[string]string{"status": "ok"})
}

func writeRepoErr(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "repo not found in this org", http.StatusNotFound)
		return
	}
	http.Error(w, "resolve repo: "+err.Error(), http.StatusInternalServerError)
}
