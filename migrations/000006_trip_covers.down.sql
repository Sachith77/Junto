DROP TABLE IF EXISTS trip_cover_uploads;
DROP TABLE IF EXISTS trip_cover_suggestions;

ALTER TABLE trips
    DROP CONSTRAINT IF EXISTS trips_cover_shape,
    DROP CONSTRAINT IF EXISTS trips_cover_source,
    DROP COLUMN IF EXISTS cover_photo_url,
    DROP COLUMN IF EXISTS cover_photographer_url,
    DROP COLUMN IF EXISTS cover_photographer_name,
    DROP COLUMN IF EXISTS cover_image_url,
    DROP COLUMN IF EXISTS cover_content_type,
    DROP COLUMN IF EXISTS cover_storage_key,
    DROP COLUMN IF EXISTS cover_source;
