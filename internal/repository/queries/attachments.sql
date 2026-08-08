-- Queries for attachments — photos, ticket screenshots and links.
--
-- Note what is absent: there is no UpdateAttachment. Attachments are immutable once ready
-- (D46) — you replace one, you do not edit it — and the only state change in their life is the
-- two-phase upload's pending -> ready confirmation, which is its own narrowly-scoped statement
-- below. Adding a general update here would quietly give the entity a merge grain the sync
-- vocabulary deliberately does not have.

-- name: CreateAttachment :one
-- The exclusive arc is enforced by the schema's attachments_one_owner CHECK, not here: passing
-- two owners produces a constraint violation that maps to a field-level error, rather than
-- silently preferring one.
INSERT INTO attachments (
  id, trip_id, slot_option_id, budget_entry_id, slot_id,
  kind, status, storage_key, external_url, content_type, original_name, uploaded_by
) VALUES (
  @id, @trip_id, @slot_option_id, @budget_entry_id, @slot_id,
  @kind, @status, @storage_key, @external_url, @content_type, @original_name, @uploaded_by
)
RETURNING *;

-- name: GetAttachmentByID :one
SELECT * FROM attachments WHERE id = @id AND deleted_at IS NULL;

-- name: ListAttachmentsForOption :many
SELECT * FROM attachments
WHERE slot_option_id = @slot_option_id AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: ListAttachmentsForBudgetEntry :many
SELECT * FROM attachments
WHERE budget_entry_id = @budget_entry_id AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: ListAttachmentsForSlot :many
SELECT * FROM attachments
WHERE slot_id = @slot_id AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: ListAttachmentsForTrip :many
-- The resync-comparison path: every live attachment in a trip in one indexed lookup.
SELECT * FROM attachments
WHERE trip_id = @trip_id AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: ConfirmAttachment :one
-- The second phase of the upload. The `status = 'pending'` predicate is what makes this
-- idempotent-safe and race-safe at once: a second confirmation, or a confirmation of something
-- already failed or deleted, affects no rows rather than resurrecting it.
--
-- Size and checksum are written from the server-side Stat, never from the client. A presigned
-- PUT cannot enforce a size limit (D48), so these are the only trustworthy values in the row
-- and they must arrive from the storage layer.
UPDATE attachments
SET status          = 'ready',
    size_bytes      = @size_bytes,
    checksum_sha256 = @checksum_sha256,
    updated_at      = @updated_at
WHERE id = @id AND status = 'pending' AND deleted_at IS NULL
RETURNING *;

-- name: MarkAttachmentFailed :execrows
-- Verification rejected the object (too large, wrong type). The row is kept rather than
-- deleted so the uploader can be told why, and the sweeper's pending filter no longer matches
-- it.
UPDATE attachments
SET status     = 'failed',
    updated_at = @updated_at
WHERE id = @id AND status = 'pending' AND deleted_at IS NULL;

-- name: SoftDeleteAttachment :execrows
-- No version predicate, because attachments have no version column (D46). Deleting an already
-- deleted attachment affects zero rows, which the caller reads as "already gone" rather than
-- as a conflict — there is no concurrent-edit case to distinguish it from.
UPDATE attachments
SET deleted_at = @deleted_at,
    updated_at = @deleted_at
WHERE id = @id AND deleted_at IS NULL;

-- name: AttachmentExists :one
SELECT EXISTS (SELECT 1 FROM attachments WHERE id = @id);

-- name: ListStalePendingAttachments :many
-- Drives the sweeper for uploads that were presigned and then abandoned. Served by the partial
-- index attachments_pending, so the scan cost is proportional to the number of pending rows
-- rather than to the table.
SELECT * FROM attachments
WHERE status = 'pending' AND created_at < @before AND deleted_at IS NULL
ORDER BY created_at
LIMIT @row_limit;
