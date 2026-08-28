# Encrypted Secret Persistence Slice

## Scope

This slice makes every public secret operation executable without changing the
approved OpenAPI shape. Secret values are accepted and returned only by the item
write/read operations. Metadata reads and lists never load or return plaintext.
Service discovery advertises `secrets` only after the complete write, read,
metadata, list, conditional-delete, and cascade behavior is wired.

## Key hierarchy and authenticated encryption

The deployment supplies one 256-bit root encryption key and a non-secret key
identifier. Mocó generates an independent random 256-bit data-encryption key for
each vault on its first secret write. HKDF-SHA-256 derives a one-time AES-256-GCM
wrapping key from the deployment root key and a random 256-bit salt. Only the
wrapped vault key, salt, and root key identifier are persisted.

For every secret ciphertext, HKDF-SHA-256 derives another one-time AES-256-GCM
key from the unwrapped vault key and a fresh random 256-bit salt. Each derived
key encrypts exactly one message; the 256-bit salt makes a derivation collision
negligible, so a fixed GCM nonce needs no distributed counter. The salt is
non-secret and is authenticated with the ciphertext.

The wrapped-key derivation context and associated data bind its algorithm/version,
tenant, and vault. The
secret associated data binds its format version, tenant, vault, logical path,
digest, and content type. Moving ciphertext or metadata between scopes therefore
fails authentication. Changing the configured root key makes existing vault keys
unreadable rather than silently producing corrupt plaintext. Root-key rotation
and multi-key read support remain a later slice; the stored key identifier makes
that migration explicit.

## Plaintext lifetime

Repository ports accept only wrapped keys and ciphertext. The SQLite adapter
never receives plaintext. Write request byte slices are cleared after the strict
handler returns, and secret JSON bodies have a bounded transport size; temporary
data-encryption keys are cleared after their cipher instance is created. Read
responses use a response wrapper that clears decrypted bytes and the local encoded
response buffer immediately after writing. The in-process root-key copy is cleared
after server shutdown. Go cannot promise that every compiler or
runtime copy is erased, so process memory and crash dumps remain sensitive.

## Persistence and concurrency

Secret rows are uniquely keyed by tenant, vault, and logical path and reference
the parent vault with an enforced composite foreign key. Versions start at one
and increase on content changes. Repeating an unconditional PUT with the same
digest and content type preserves the version and ETag, while changed content
increments both. `If-None-Match: *` is create-only; `If-Match` consumes one exact
version. Revision-qualified SQL resolves concurrent writers so only one caller
can consume a given ETag.

Insertion sequence supplies stable list upper bounds. Signed cursors bind the
tenant, vault, prefix, and expiry. Listing selects metadata columns only. Secret
digests are SHA-256 over decoded values and are intended for drift detection, not
for recovering values.

## Deletion semantics

Deleting a vault without `cascade=true` returns 409 while secrets remain. With
cascade enabled, SQLite foreign keys delete secret rows and the wrapped vault key
in the same transaction. Tenant cascade continues through vaults to both child
tables. A missing secret returns 404, matching the contract's reconciliation
guidance.

## Deferred security work

This slice does not add root-key rotation, external KMS/HSM providers, audit
records, Casbin authorization, multi-principal authentication, backup policy, or
production deployment hardening. Those capabilities must preserve the ciphertext
and plaintext-lifetime invariants above.
