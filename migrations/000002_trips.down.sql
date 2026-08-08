-- Drop in reverse dependency order. slots.selected_option_id is a circular FK into
-- slot_options, so that constraint must go before slot_options can be dropped.
ALTER TABLE IF EXISTS slots DROP CONSTRAINT IF EXISTS slots_selected_option_fk;

DROP TABLE IF EXISTS option_votes;
DROP TABLE IF EXISTS slot_options;
DROP TABLE IF EXISTS slots;
DROP TABLE IF EXISTS days;
DROP TABLE IF EXISTS trip_invitations;
DROP TABLE IF EXISTS trip_members;
DROP TABLE IF EXISTS trips;
