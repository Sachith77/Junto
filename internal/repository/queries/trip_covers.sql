-- Cached destination suggestions.

-- name: GetTripCoverSuggestionByQueryKey :one
SELECT * FROM trip_cover_suggestions
WHERE query_key = @query_key AND expires_at > @now;

-- name: GetTripCoverSuggestionByID :one
SELECT * FROM trip_cover_suggestions WHERE id = @id;

-- name: UpsertTripCoverSuggestion :one
INSERT INTO trip_cover_suggestions (
    id, query_key, query, found, image_url, photographer_name, photographer_url,
    photo_url, download_location, expires_at
) VALUES (
    @id, @query_key, @query, @found, @image_url, @photographer_name, @photographer_url,
    @photo_url, @download_location, @expires_at
)
ON CONFLICT (query_key) DO UPDATE SET
    query = EXCLUDED.query,
    found = EXCLUDED.found,
    image_url = EXCLUDED.image_url,
    photographer_name = EXCLUDED.photographer_name,
    photographer_url = EXCLUDED.photographer_url,
    photo_url = EXCLUDED.photo_url,
    download_location = EXCLUDED.download_location,
    expires_at = EXCLUDED.expires_at,
    updated_at = now()
RETURNING *;

-- Upload lifecycle.

-- name: CreateTripCoverUpload :one
INSERT INTO trip_cover_uploads (
    id, trip_id, storage_key, content_type, original_name, uploaded_by
) VALUES (@id, @trip_id, @storage_key, @content_type, @original_name, @uploaded_by)
RETURNING *;

-- name: GetTripCoverUploadByID :one
SELECT * FROM trip_cover_uploads WHERE id = @id;

-- name: ConfirmTripCoverUpload :execrows
UPDATE trip_cover_uploads
SET status = 'ready', size_bytes = @size_bytes, updated_at = @updated_at
WHERE id = @id AND status = 'pending';

-- name: FailTripCoverUpload :execrows
UPDATE trip_cover_uploads
SET status = 'failed', updated_at = @updated_at
WHERE id = @id AND status = 'pending';
