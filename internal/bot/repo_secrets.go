package bot

import (
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
//	GET  /api/repo-secrets?owner=X&name=Y[&path=Z]
//	PUT  /api/repo-secrets   body {owner, name, path, secret_name, value}
//	DELETE /api/repo-secrets?owner=X&name=Y&secret_name=K[&path=Z]
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

	rows, err := b.store.Queries.ListRepoSecretValues(r.Context(), sqlc.ListRepoSecretValuesParams{
		InstallationID: repo.InstallationID,
		RepoID:         repo.RepoID,
		Path:           path,
	})
	if err != nil {
		http.Error(w, "list secrets: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := repoSecretsListResponse{
		Owner: owner, Repo: name, Path: path,
		Secrets: make([]repoSecretEntry, 0, len(rows)),
	}
	for _, row := range rows {
		resp.Secrets = append(resp.Secrets, repoSecretEntry{
			Name:   row.Name,
			Filled: len(row.ValueEncrypted) > 0,
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
	if err := b.store.Queries.DeleteRepoSecretValue(r.Context(), sqlc.DeleteRepoSecretValueParams{
		InstallationID: repo.InstallationID,
		RepoID:         repo.RepoID,
		Path:           path,
		Name:           secretName,
	}); err != nil {
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
	return b.store.Queries.GetGithubRepoForOrg(r.Context(), sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID,
		Owner: owner,
		Name:  name,
	})
}

func writeRepoErr(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "repo not found in this org", http.StatusNotFound)
		return
	}
	http.Error(w, "resolve repo: "+err.Error(), http.StatusInternalServerError)
}
