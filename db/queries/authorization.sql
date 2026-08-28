-- name: ListAuthorizationRoleBindings :many
SELECT principal_id, role, domain
FROM authorization_role_bindings
ORDER BY principal_id, role, domain;

-- name: ListAuthorizationPolicies :many
SELECT subject, domain, path, method
FROM authorization_policies
ORDER BY subject, domain, path, method;

-- name: GetAuthorizationPolicyState :one
SELECT initialized
FROM authorization_policy_state
WHERE id = 1;

-- name: DeleteAuthorizationRoleBindings :exec
DELETE FROM authorization_role_bindings;

-- name: DeleteAuthorizationPolicies :exec
DELETE FROM authorization_policies;

-- name: InsertAuthorizationRoleBinding :exec
INSERT INTO authorization_role_bindings (principal_id, role, domain)
VALUES (?, ?, ?);

-- name: InsertAuthorizationPolicy :exec
INSERT INTO authorization_policies (subject, domain, path, method)
VALUES (?, ?, ?, ?);

-- name: MarkAuthorizationPolicyStateInitialized :exec
UPDATE authorization_policy_state
SET initialized = 1
WHERE id = 1;
