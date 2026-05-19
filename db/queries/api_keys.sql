-- name: CreateOrgAPIKey :one
INSERT INTO org_api_keys (
    id, org_id, name, key_prefix, key_hash, created_by
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING id, org_id, name, key_prefix, key_hash, created_by,
          created_at, last_used_at, revoked_at;

-- name: ListOrgAPIKeys :many
SELECT id, org_id, name, key_prefix, key_hash, created_by,
       created_at, last_used_at, revoked_at
FROM org_api_keys
WHERE org_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: GetOrgAPIKeyByHash :one
SELECT id, org_id, name, key_prefix, key_hash, created_by,
       created_at, last_used_at, revoked_at
FROM org_api_keys
WHERE key_hash = $1 AND revoked_at IS NULL;

-- name: TouchOrgAPIKeyLastUsed :exec
UPDATE org_api_keys
   SET last_used_at = NOW()
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeOrgAPIKey :execrows
UPDATE org_api_keys
   SET revoked_at = NOW()
WHERE org_id = $1 AND id = $2 AND revoked_at IS NULL;

-- name: DeleteOrgAPIKeysByOrg :exec
DELETE FROM org_api_keys WHERE org_id = $1;
