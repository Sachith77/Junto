-- Trip cover photos: private user uploads plus cached destination suggestions.

ALTER TABLE trips
    ADD COLUMN cover_source text NOT NULL DEFAULT 'legacy',
    ADD COLUMN cover_storage_key text NOT NULL DEFAULT '',
    ADD COLUMN cover_content_type text NOT NULL DEFAULT '',
    ADD COLUMN cover_image_url text NOT NULL DEFAULT '',
    ADD COLUMN cover_photographer_name text NOT NULL DEFAULT '',
    ADD COLUMN cover_photographer_url text NOT NULL DEFAULT '',
    ADD COLUMN cover_photo_url text NOT NULL DEFAULT '',
    ADD CONSTRAINT trips_cover_source CHECK (
        cover_source IN ('legacy', 'neutral', 'suggested', 'uploaded')
    ),
    ADD CONSTRAINT trips_cover_shape CHECK (
        (cover_source IN ('legacy', 'neutral') AND cover_storage_key = '' AND cover_image_url = '') OR
        (cover_source = 'suggested' AND cover_storage_key = '' AND cover_image_url <> '') OR
        (cover_source = 'uploaded' AND cover_storage_key <> '' AND cover_image_url = '')
    );

-- Search responses are cached by normalized query. A row with found=false is a negative cache
-- entry, preventing an empty or failed destination from being requested on every keystroke.
CREATE TABLE trip_cover_suggestions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    query_key         text NOT NULL UNIQUE,
    query             text NOT NULL,
    found             boolean NOT NULL,
    image_url         text NOT NULL DEFAULT '',
    photographer_name text NOT NULL DEFAULT '',
    photographer_url  text NOT NULL DEFAULT '',
    photo_url         text NOT NULL DEFAULT '',
    download_location text NOT NULL DEFAULT '',
    expires_at        timestamptz NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT trip_cover_suggestions_shape CHECK (
        (found AND image_url <> '' AND photographer_name <> '' AND photographer_url <> '' AND photo_url <> '') OR
        (NOT found AND image_url = '' AND photographer_name = '' AND photographer_url = '' AND photo_url = '')
    )
);

CREATE INDEX trip_cover_suggestions_expiry ON trip_cover_suggestions (expires_at);

-- Uploads have the same two-phase lifecycle as attachments, but belong directly to a trip.
-- Keeping them separate preserves attachments' exactly-one slot/option/budget owner invariant.
CREATE TABLE trip_cover_uploads (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id       uuid NOT NULL REFERENCES trips (id) ON DELETE CASCADE,
    storage_key   text NOT NULL UNIQUE,
    content_type  text NOT NULL,
    original_name text NOT NULL DEFAULT '',
    size_bytes    bigint,
    status        text NOT NULL DEFAULT 'pending',
    uploaded_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT trip_cover_uploads_status CHECK (status IN ('pending', 'ready', 'failed')),
    CONSTRAINT trip_cover_uploads_size CHECK (size_bytes IS NULL OR size_bytes >= 0),
    CONSTRAINT trip_cover_uploads_name_len CHECK (char_length(original_name) <= 255)
);

CREATE INDEX trip_cover_uploads_trip ON trip_cover_uploads (trip_id, created_at DESC);
CREATE INDEX trip_cover_uploads_pending ON trip_cover_uploads (created_at) WHERE status = 'pending';
