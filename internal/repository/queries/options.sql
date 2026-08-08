-- Queries for slot_options — the candidate proposals under a slot.

-- name: CreateSlotOption :one
INSERT INTO slot_options (
  id, slot_id, trip_id, title, notes, external_url, estimated_cost_minor,
  place_name, place_address, place_lat, place_lng, place_provider_id, proposed_by
) VALUES (
  @id, @slot_id, @trip_id, @title, @notes, @external_url, @estimated_cost_minor,
  @place_name, @place_address, @place_lat, @place_lng, @place_provider_id, @proposed_by
)
RETURNING *;

-- name: GetSlotOptionByID :one
SELECT * FROM slot_options WHERE id = @id AND deleted_at IS NULL;

-- name: ListOptionsForSlot :many
-- Ordered by creation, not by a fractional index. Reordering candidates is not a gesture the
-- product needs; if it becomes one, the position scheme applies identically as an added
-- column. The id tiebreak keeps the order total when two options share a timestamp.
SELECT * FROM slot_options
WHERE slot_id = @slot_id AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: ListOptionsForTrip :many
-- The Stage 2 resync path: every option in a sync room, in one indexed lookup with no join.
-- This is what the denormalised trip_id buys.
SELECT * FROM slot_options
WHERE trip_id = @trip_id AND deleted_at IS NULL
ORDER BY slot_id, created_at, id;

-- name: UpdateSlotOption :one
UPDATE slot_options
SET title                = @title,
    notes                = @notes,
    external_url         = @external_url,
    estimated_cost_minor = @estimated_cost_minor,
    place_name           = @place_name,
    place_address        = @place_address,
    place_lat            = @place_lat,
    place_lng            = @place_lng,
    place_provider_id    = @place_provider_id,
    version              = version + 1,
    updated_at           = @updated_at
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteSlotOption :execrows
-- Tombstoned, like a slot. Note what the schema does on top of this: if the deleted option
-- was the slot's selection, slots.selected_option_id is NOT cleared here, because a soft
-- delete leaves the row present and the FK satisfied. Clearing a stale selection is a
-- service-layer decision, made explicitly rather than as a side effect of a DELETE.
UPDATE slot_options
SET deleted_at = @deleted_at,
    version    = version + 1,
    updated_at = @deleted_at
WHERE id = @id AND version = @version AND deleted_at IS NULL;

-- name: SlotOptionExists :one
SELECT EXISTS (SELECT 1 FROM slot_options WHERE id = @id);

-- name: CountLiveOptionsForSlot :one
SELECT count(*) FROM slot_options WHERE slot_id = @slot_id AND deleted_at IS NULL;
