# Vault Lifecycle Slice

## Scope

This slice implements the public tenant-scoped vault lifecycle while leaving all
secret operations unavailable. Service discovery advertises vault support only
after every create, list, get, replace, and delete route is executable.

## Persistence and isolation

Vault rows reference tenants with an enforced SQLite foreign key. Names and
external IDs are unique within one tenant, while identical values remain valid in
different tenants. Every repository lookup and mutation includes both tenant and
vault IDs so a caller cannot address a vault through another tenant's route.

Insertion sequence supplies the upper bound for stable pagination. Signed cursors
also bind the parent tenant and exact filters, preventing reuse across tenants.
Vault creation idempotency is scoped to principal and operation; the canonical
request hash includes the tenant ID.

## Deletion semantics

Deleting a tenant without `cascade=true` uses an atomic `NOT EXISTS` predicate and
returns 409 while vaults remain. Explicit cascade relies on the database foreign
key to remove all child vaults in the same transaction. Vaults are necessarily
empty until the secret persistence slice lands; that later slice must apply the
same conflict-versus-cascade rule to secret children.

## Concurrency and contract mapping

Vault ETags carry the vault revision. Conditional updates and deletes execute
revision-qualified SQL, so only one writer can consume an ETag. Uniqueness,
missing-parent, missing-vault, child-conflict, cursor, and precondition failures
map to the response types declared in the generated strict OpenAPI interface.

## Deferred security work

Vaults currently contain identity and metadata only. Secret encryption, key
hierarchy, plaintext lifetime, audit records, and Casbin authorization remain
deferred, so this is still not a secret-management-ready release.
