# Supabase Local Development Research

**Date:** 2026-05-01  
**Question:** Can Supabase support a local development experience for team members who don't want to use the cloud?

---

## Short Answer

Yes. Supabase has first-class local development support via the Supabase CLI. Each developer can run the full Supabase stack on their machine using Docker, with no cloud dependency.

---

## Local Development via Supabase CLI

Each developer can run the **entire Supabase stack locally** using the CLI. The only prerequisite is Docker.

```bash
# Install CLI
brew install supabase/tap/supabase

# Initialize in your project
supabase init

# Spin up the full local stack
supabase start
```

This starts all services in Docker containers:
- Postgres database
- Auth
- Storage
- Edge Functions runtime
- Studio dashboard at `http://localhost:54323` (same UI as Supabase Cloud)

---

## Team Workflow

The `supabase/` directory created by `supabase init` is committed to version control. The typical flow:

1. Developer makes DB changes locally via Studio or SQL
2. `supabase db diff` captures changes as a migration file
3. Migration is committed and pushed
4. Teammates run `supabase db reset` to apply new migrations locally
5. TypeScript types regenerated with `supabase gen types typescript`

---

## Local Dev vs. Self-Hosting

| | Local Dev (CLI) | Self-Hosting (Docker Compose) |
|---|---|---|
| Purpose | Development & testing only | Production-ready deployment |
| Setup | `supabase start` | Full `docker-compose.yml` |
| Expose to external traffic? | No | Yes, with proper config |

> **Note:** The local CLI stack should **not** be exposed to external traffic — it is for dev/test only.
> For a shared non-cloud environment, Supabase supports full self-hosting via Docker Compose (11 containerized services behind a Kong gateway).

---

## Recommendation

Use Supabase Cloud for staging/production, while each developer runs a full local stack with `supabase start`. Schema changes are shared via migration files in git, keeping everyone in sync.

---

## Sources

- [Local Development & CLI | Supabase Docs](https://supabase.com/docs/guides/local-development)
- [Local development with schema migrations | Supabase Docs](https://supabase.com/docs/guides/local-development/overview)
- [Supabase CLI | Supabase Docs](https://supabase.com/docs/guides/local-development/cli/getting-started)
- [Self-Hosting with Docker | Supabase Docs](https://supabase.com/docs/guides/self-hosting/docker)
- [Self-Hosting | Supabase Docs](https://supabase.com/docs/guides/self-hosting)
- [How to Use Supabase CLI: The Complete Developer Guide (2026)](https://apidog.com/blog/supabase-cli/)
- [Developer Update - March 2026 | Supabase GitHub Discussions](https://github.com/orgs/supabase/discussions/43465)
