-- name: CreateLocalAuthUser :one
INSERT INTO local_auth_users (
    id, email, email_normalized, password_hash, first_name, last_name
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING id, email, email_normalized, password_hash, first_name, last_name, created_at, updated_at;

-- name: GetLocalAuthUserByID :one
SELECT id, email, email_normalized, password_hash, first_name, last_name, created_at, updated_at
FROM local_auth_users
WHERE id = $1;

-- name: GetLocalAuthUserByEmail :one
SELECT id, email, email_normalized, password_hash, first_name, last_name, created_at, updated_at
FROM local_auth_users
WHERE email_normalized = $1;

-- name: UpdateLocalAuthUserProfile :one
UPDATE local_auth_users
SET first_name = $2,
    last_name = $3,
    updated_at = NOW()
WHERE id = $1
RETURNING id, email, email_normalized, password_hash, first_name, last_name, created_at, updated_at;

-- name: UpdateLocalAuthUserPassword :exec
UPDATE local_auth_users
SET password_hash = $2,
    updated_at = NOW()
WHERE id = $1;

-- name: DeleteLocalAuthUser :exec
DELETE FROM local_auth_users WHERE id = $1;

-- name: CreateLocalAuthOrg :one
INSERT INTO local_auth_orgs (id, name)
VALUES ($1, $2)
RETURNING id, name, created_at, updated_at;

-- name: GetLocalAuthOrg :one
SELECT id, name, created_at, updated_at
FROM local_auth_orgs
WHERE id = $1;

-- name: UpdateLocalAuthOrgName :one
UPDATE local_auth_orgs
SET name = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING id, name, created_at, updated_at;

-- name: DeleteLocalAuthOrg :exec
DELETE FROM local_auth_orgs WHERE id = $1;

-- name: CreateLocalAuthMembership :one
INSERT INTO local_auth_memberships (id, user_id, org_id, role_slug)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, org_id) DO UPDATE SET
    role_slug = EXCLUDED.role_slug,
    updated_at = NOW()
RETURNING id, user_id, org_id, role_slug, created_at, updated_at;

-- name: GetLocalAuthMembership :one
SELECT id, user_id, org_id, role_slug, created_at, updated_at
FROM local_auth_memberships
WHERE id = $1;

-- name: GetLocalAuthMembershipForUserOrg :one
SELECT id, user_id, org_id, role_slug, created_at, updated_at
FROM local_auth_memberships
WHERE user_id = $1 AND org_id = $2;

-- name: ListLocalAuthUserOrgs :many
SELECT
    m.id AS membership_id,
    m.user_id,
    m.org_id,
    m.role_slug,
    o.name AS org_name
FROM local_auth_memberships m
JOIN local_auth_orgs o ON o.id = m.org_id
WHERE m.user_id = $1
ORDER BY o.name, o.id;

-- name: CountLocalAuthUserOrgs :one
SELECT COUNT(*)::int
FROM local_auth_memberships
WHERE user_id = $1;

-- name: ListLocalAuthMembers :many
SELECT
    m.id AS membership_id,
    m.user_id,
    m.org_id,
    m.role_slug,
    u.email,
    u.first_name,
    u.last_name
FROM local_auth_memberships m
JOIN local_auth_users u ON u.id = m.user_id
WHERE m.org_id = $1
ORDER BY lower(u.email), u.id;

-- name: CountLocalAuthAdmins :one
SELECT COUNT(*)::int
FROM local_auth_memberships
WHERE org_id = $1 AND role_slug = 'admin';

-- name: UpdateLocalAuthMembershipRole :one
UPDATE local_auth_memberships
SET role_slug = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING id, user_id, org_id, role_slug, created_at, updated_at;

-- name: DeleteLocalAuthMembership :exec
DELETE FROM local_auth_memberships WHERE id = $1;

-- name: FindLocalAuthOrgUserByEmail :one
SELECT u.id, u.email, u.email_normalized, u.password_hash, u.first_name, u.last_name, u.created_at, u.updated_at
FROM local_auth_users u
JOIN local_auth_memberships m ON m.user_id = u.id
WHERE m.org_id = $1 AND u.email_normalized = $2;

-- name: CreateLocalAuthInvitation :one
INSERT INTO local_auth_invitations (
    id, org_id, email, email_normalized, role_slug, token_hash, expires_at, created_by
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING id, org_id, email, email_normalized, role_slug, token_hash, expires_at, accepted_at, revoked_at, created_by, created_at, updated_at;

-- name: ListLocalAuthInvitations :many
SELECT id, org_id, email, email_normalized, role_slug, token_hash, expires_at, accepted_at, revoked_at, created_by, created_at, updated_at
FROM local_auth_invitations
WHERE org_id = $1
  AND accepted_at IS NULL
  AND revoked_at IS NULL
  AND expires_at > NOW()
ORDER BY created_at DESC;

-- name: GetLocalAuthInvitationByTokenHash :one
SELECT id, org_id, email, email_normalized, role_slug, token_hash, expires_at, accepted_at, revoked_at, created_by, created_at, updated_at
FROM local_auth_invitations
WHERE token_hash = $1;

-- name: GetLocalAuthInvitation :one
SELECT id, org_id, email, email_normalized, role_slug, token_hash, expires_at, accepted_at, revoked_at, created_by, created_at, updated_at
FROM local_auth_invitations
WHERE id = $1;

-- name: AcceptLocalAuthInvitation :exec
UPDATE local_auth_invitations
SET accepted_at = NOW(),
    updated_at = NOW()
WHERE id = $1
  AND accepted_at IS NULL
  AND revoked_at IS NULL
  AND expires_at > NOW();

-- name: RevokeLocalAuthInvitation :exec
UPDATE local_auth_invitations
SET revoked_at = NOW(),
    updated_at = NOW()
WHERE id = $1
  AND org_id = $2
  AND accepted_at IS NULL
  AND revoked_at IS NULL;

-- name: CreateLocalAuthSession :one
INSERT INTO local_auth_sessions (
    id, user_id, token_hash, active_org_id, expires_at
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING id, user_id, token_hash, active_org_id, expires_at, last_seen_at, created_at;

-- name: GetLocalAuthSession :one
SELECT
    s.id,
    s.user_id,
    s.token_hash,
    s.active_org_id,
    s.expires_at,
    s.last_seen_at,
    s.created_at,
    u.email,
    u.first_name,
    u.last_name,
    m.role_slug
FROM local_auth_sessions s
JOIN local_auth_users u ON u.id = s.user_id
LEFT JOIN local_auth_memberships m
    ON m.user_id = s.user_id
   AND m.org_id = s.active_org_id
WHERE s.id = $1
  AND s.expires_at > NOW();

-- name: TouchLocalAuthSession :exec
UPDATE local_auth_sessions
SET last_seen_at = NOW()
WHERE id = $1;

-- name: UpdateLocalAuthSessionOrg :one
UPDATE local_auth_sessions
SET active_org_id = $2,
    last_seen_at = NOW()
WHERE id = $1
RETURNING id, user_id, token_hash, active_org_id, expires_at, last_seen_at, created_at;

-- name: DeleteLocalAuthSession :exec
DELETE FROM local_auth_sessions WHERE id = $1;

-- name: DeleteLocalAuthSessionsForUser :exec
DELETE FROM local_auth_sessions WHERE user_id = $1;

-- name: DeleteExpiredLocalAuthSessions :exec
DELETE FROM local_auth_sessions WHERE expires_at <= NOW();
