-- GitHub personal access token connection. Orgs can connect GitHub by
-- pasting a PAT instead of (or alongside) installing the GitHub App.
-- The token is AES-GCM ciphertext like the other org_configs secrets.
-- Repos discovered through the PAT are cached in github_repos under a
-- synthetic installation row whose installation_id is a stable
-- negative value derived from the org id, so every query that joins
-- installations → repos keeps working unchanged.
ALTER TABLE org_configs
    ADD COLUMN github_pat_encrypted BYTEA;
