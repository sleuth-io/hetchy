package auth

import (
	"html/template"
	"net/http"
)

type localAuthPageData struct {
	Mode          string
	Title         string
	Eyebrow       string
	SubmitLabel   string
	AlternateText string
	AlternateURL  string
	AlternateLink string
	Email         string
	FirstName     string
	LastName      string
	InviteToken   string
	InviteEmail   string
	InviteRole    string
	CSRFToken     string
	Error         string
}

func (s *Service) renderLocalAuthPage(w http.ResponseWriter, data localAuthPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if data.CSRFToken == "" {
		token, err := s.issueLocalAuthCSRFToken(w)
		if err != nil {
			http.Error(w, "render auth page: "+err.Error(), http.StatusInternalServerError)
			return
		}
		data.CSRFToken = token
	}
	if err := localAuthTemplate.Execute(w, data); err != nil {
		http.Error(w, "render auth page: "+err.Error(), http.StatusInternalServerError)
	}
}

var localAuthTemplate = template.Must(template.New("local-auth").Parse(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} - Hetchy</title>
<script src="/assets/theme_bootstrap.js"></script>
<link rel="stylesheet" href="/assets/landing.css">
<style>
.auth-form{display:grid;gap:.8rem;margin-top:1.1rem}
.auth-form label{display:grid;gap:.3rem;color:#4f5a68;font-size:.84rem;font-weight:600}
.auth-form input{width:100%;min-height:2.55rem;border:1px solid #cad5e3;border-radius:8px;padding:.65rem .75rem;background:#fff;color:#111418;font:inherit}
.auth-form input:focus{outline:none;border-color:#0b93f6;box-shadow:0 0 0 3px rgba(11,147,246,.14)}
.auth-form .name-row{display:grid;grid-template-columns:1fr 1fr;gap:.75rem}
.auth-form button{min-height:2.75rem;border:0;border-radius:8px;background:#0b93f6;color:#fff;font:inherit;font-weight:650;cursor:pointer}
.auth-form button:hover{background:#087fd8}
.auth-note{margin:.9rem 0 0;color:#5e6878;font-size:.9rem;line-height:1.45}
.auth-note a{color:#0b5ec0;font-weight:650}
.auth-alert{margin:1rem 0 0;padding:.7rem .8rem;border-radius:8px;background:#fff1f1;color:#9f1d1d;border:1px solid #ffd0d0;font-size:.9rem}
.invite-banner{margin:1rem 0 0;padding:.7rem .8rem;border-radius:8px;background:#eff8ff;color:#164b78;border:1px solid #cfe8ff;font-size:.9rem}
html.is-dark .auth-form label{color:#c6c6cb}
html.is-dark .auth-form input{background:#1c1c1f;color:#f5f5f7;border-color:#383842}
html.is-dark .auth-form input:focus{border-color:#7dc4ff;box-shadow:0 0 0 3px rgba(125,196,255,.12)}
html.is-dark .auth-form button{background:#7dc4ff;color:#101317}
html.is-dark .auth-form button:hover{background:#9fd3ff}
html.is-dark .auth-note{color:#b8b8bc}
html.is-dark .auth-note a{color:#7dc4ff}
html.is-dark .auth-alert{background:#331717;color:#ffb4b4;border-color:#5b2828}
html.is-dark .invite-banner{background:#16273a;color:#cde8ff;border-color:#24445f}
@media (max-width:480px){.auth-form .name-row{grid-template-columns:1fr}}
</style>
</head>
<body>
<main class="entry-shell">
  <section class="entry-panel" aria-labelledby="auth-title">
    <a class="brand" href="/" aria-label="Hetchy home">
      <img class="brand-mark" src="data:image/svg+xml;utf8,%3Csvg%20xmlns%3D%27http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg%27%20viewBox%3D%270%200%2032%2032%27%3E%3Crect%20width%3D%2732%27%20height%3D%2732%27%20rx%3D%276%27%20fill%3D%27%230d1117%27%2F%3E%3Cg%20fill%3D%27%237dc4ff%27%3E%3Crect%20x%3D%276%27%20y%3D%276%27%20width%3D%276%27%20height%3D%2720%27%2F%3E%3Crect%20x%3D%2720%27%20y%3D%276%27%20width%3D%276%27%20height%3D%2720%27%2F%3E%3Crect%20x%3D%276%27%20y%3D%2714%27%20width%3D%2720%27%20height%3D%274%27%2F%3E%3C%2Fg%3E%3C%2Fsvg%3E" alt="">
      <span>Hetchy</span>
    </a>
    <div class="entry-copy">
      <p class="eyebrow">{{.Eyebrow}}</p>
      <h1 id="auth-title">{{.Title}}</h1>
      <p class="lede">Use your email and password to access Hetchy.</p>
    </div>
    {{if .Error}}<div class="auth-alert">{{.Error}}</div>{{end}}
    {{if .InviteEmail}}<div class="invite-banner">Invitation for {{.InviteEmail}} as {{.InviteRole}}.</div>{{end}}
    <form method="POST" class="auth-form" action="/{{.Mode}}">
      <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
      {{if .InviteToken}}<input type="hidden" name="invite" value="{{.InviteToken}}">{{end}}
      {{if eq .Mode "signup"}}
      <div class="name-row">
        <label>First name
          <input type="text" name="first_name" value="{{.FirstName}}" autocomplete="given-name">
        </label>
        <label>Last name
          <input type="text" name="last_name" value="{{.LastName}}" autocomplete="family-name">
        </label>
      </div>
      {{end}}
      <label>Email
        <input type="email" name="email" value="{{.Email}}" autocomplete="email" required>
      </label>
      <label>Password
        <input type="password" name="password" autocomplete="{{if eq .Mode "signup"}}new-password{{else}}current-password{{end}}" required minlength="8">
      </label>
      <button type="submit">{{.SubmitLabel}}</button>
    </form>
    <p class="auth-note">{{.AlternateText}} <a href="{{.AlternateURL}}{{if .InviteToken}}?invite={{.InviteToken}}{{end}}">{{.AlternateLink}}</a>.</p>
  </section>
</main>
</body>
</html>`))
