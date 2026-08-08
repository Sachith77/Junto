package repository

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junto/junto/internal/domain"
)

// Translating Postgres errors into domain errors happens here and nowhere else.
//
// The alternative — letting a *pgconn.PgError reach the service or transport layer — would
// mean the HTTP layer has to know SQLSTATE codes to pick a status code, which puts database
// knowledge in the two places that must not have it.

// Postgres error classes we care about. Referenced by name rather than by literal so the
// call sites read as intent.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
	pgNotNullViolation    = "23502"
	pgStringDataTruncated = "22001"
	pgSerializationFail   = "40001"
	pgDeadlockDetected    = "40P01"
)

// constraintViolations maps a constraint name to the field-level message a user should see.
//
// Mapping by NAME rather than parsing the driver's message text is the whole point: the
// message text is a Postgres implementation detail that changes between versions and
// locales, while the constraint name is something we chose and control. It also means a
// constraint added without a mapping degrades to a generic conflict rather than leaking
// database internals into an API response.
var constraintViolations = map[string]domain.FieldViolation{
	"users_email_lower_uq": {
		Field: "email", Code: "email_taken",
		Message: "an account with this email already exists",
	},
	"trip_members_uq": {
		Field: "user_id", Code: "already_member",
		Message: "this user is already a member of the trip",
	},
	"trip_members_owner_uq": {
		Field: "role", Code: "owner_exists",
		Message: "this trip already has an owner; transfer ownership instead",
	},
	"days_trip_date_uq": {
		Field: "date", Code: "date_taken",
		Message: "this trip already has a day on that date",
	},

	// The composite FKs that make cross-trip and cross-slot references unrepresentable.
	// Mapping each to the field the user actually supplied is the whole reason this table
	// exists — the alternative is a generic conflict that tells them nothing.
	"slots_day_fk": {
		Field: "day_id", Code: "day_not_in_trip",
		Message: "the day does not belong to this trip",
	},
	"slots_selected_option_fk": {
		Field: "selected_option_id", Code: "option_not_in_slot",
		Message: "that option belongs to a different slot",
	},
	"slot_options_slot_fk": {
		Field: "slot_id", Code: "slot_not_in_trip",
		Message: "the slot does not belong to this trip",
	},
	"option_votes_option_fk": {
		Field: "option_id", Code: "option_not_in_slot",
		Message: "you can only vote for an option belonging to this slot",
	},
	"option_votes_slot_fk": {
		Field: "slot_id", Code: "slot_not_in_trip",
		Message: "the slot does not belong to this trip",
	},
	"option_votes_uq": {
		Field: "user_id", Code: "already_voted",
		Message: "this member already has a vote on this slot",
	},
	"budget_entries_option_fk": {
		Field: "slot_option_id", Code: "option_not_in_trip",
		Message: "that option belongs to a different trip",
	},

	"slots_times": {
		Field: "end_time", Code: "before_start",
		Message: "end time must not be before start time",
	},
	"slots_status": {
		Field: "status", Code: "invalid_status",
		Message: "status must be one of: planned, covered, skipped",
	},
	"slots_kind": {
		Field: "kind", Code: "invalid_kind",
		Message: "kind must be one of: place, activity, transport, lodging, note",
	},
	"slot_options_latlng": {
		Field: "place", Code: "incomplete_coordinates",
		Message: "latitude and longitude must be provided together",
	},
	"slot_options_cost": {
		Field: "estimated_cost", Code: "negative",
		Message: "an estimate must not be negative",
	},

	// Budget. The sum check is a DEFERRED constraint trigger, so it fires at COMMIT rather
	// than at the offending statement — which means this mapping is what turns a
	// commit-time failure into something a user can act on.
	"budget_entries_amount": {
		Field: "amount", Code: "negative",
		Message: "amount must not be negative",
	},
	"budget_entries_category": {
		Field: "category", Code: "invalid_category",
		Message: "category must be one of: lodging, transport, food, activity, shopping, other",
	},
	"budget_splits_uq": {
		Field: "splits", Code: "duplicate_member",
		Message: "a member may appear at most once in a split",
	},

	// Attachments.
	"attachments_one_owner": {
		Field: "owner", Code: "exactly_one_required",
		Message: "an attachment must belong to exactly one of: slot option, budget entry, slot",
	},
	"attachments_shape": {
		Field: "kind", Code: "invalid_shape",
		Message: "a file needs a storage key and a link needs a URL",
	},
	"attachments_storage_key_uq": {
		Field: "storage_key", Code: "already_used",
		Message: "that storage object is already attached",
	},

	// Comments.
	"comments_slot_fk": {
		Field: "slot_id", Code: "slot_not_in_trip",
		Message: "the slot does not belong to this trip",
	},
	"comments_body_len": {
		Field: "body", Code: "invalid_length",
		Message: "comment must be between 1 and 4000 characters",
	},

	"trips_base_currency": {
		Field: "base_currency", Code: "invalid_currency",
		Message: "currency must be a three-letter ISO 4217 code",
	},
	"trip_invitations_role": {
		Field: "role", Code: "invalid_role",
		Message: "invitation role must be editor or viewer",
	},
	"trips_dates_ordered": {
		Field: "end_date", Code: "before_start",
		Message: "end date must not be before start date",
	},
}

// mapError converts a driver error into a domain error.
//
// entity names what was being operated on, so a "not found" reaching a log has enough
// context to be actionable without a stack trace.
func mapError(entity string, err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", entity, domain.ErrNotFound)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("%s: %w", entity, err)
	}

	if v, ok := constraintViolations[pgErr.ConstraintName]; ok {
		// A known constraint becomes a field-level validation error, so the client learns
		// which input to fix rather than being told "conflict".
		ve := &domain.ValidationError{}
		ve.Add(v.Field, v.Code, v.Message)
		return ve
	}

	switch pgErr.Code {
	case pgUniqueViolation:
		return fmt.Errorf("%s: %w (constraint %s)", entity, domain.ErrAlreadyExists, pgErr.ConstraintName)

	case pgForeignKeyViolation:
		// A dangling reference means the thing being pointed at is not visible to this
		// caller — either it does not exist or they may not see it. Not-found is the right
		// answer for both, and it avoids confirming the existence of records the caller has
		// no access to.
		return fmt.Errorf("%s: %w (constraint %s)", entity, domain.ErrNotFound, pgErr.ConstraintName)

	case pgCheckViolation, pgNotNullViolation, pgStringDataTruncated:
		// Reaching here means domain validation let something through that the database
		// then rejected. The request is still refused correctly, but the two layers have
		// drifted and the constraint name in the message is the lead for fixing it.
		ve := &domain.ValidationError{}
		ve.Add(pgErr.ColumnName, "constraint_violation",
			fmt.Sprintf("value rejected by database constraint %q", pgErr.ConstraintName))
		return ve

	case pgSerializationFail, pgDeadlockDetected:
		// Transient and safely retryable by the caller. Surfaced as a version conflict
		// because that is what it is from the client's point of view: someone else wrote
		// first, try again with fresh state.
		return fmt.Errorf("%s: %w (%s)", entity, domain.ErrVersionConflict, pgErr.Code)

	default:
		return fmt.Errorf("%s: %w", entity, err)
	}
}

// isNoRows reports whether an error is pgx's "no rows" sentinel.
//
// Queries that RETURNING a row surface an optimistic-concurrency miss as ErrNoRows rather
// than a zero row count, so this is the trigger for the resolveWriteMiss follow-up.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// resolveWriteMiss explains why an optimistic update or delete affected zero rows.
//
// Two very different situations produce the same zero-row result: the row does not exist, or
// it exists at a different version. Collapsing them would make an API return 404 for what is
// really a concurrent-edit conflict, and a client that retried a 404 would give up on data
// that is still there.
//
// The extra query runs only on the failure path, so the common case costs nothing.
func resolveWriteMiss(entity string, exists bool) error {
	if exists {
		return fmt.Errorf("%s: %w", entity, domain.ErrVersionConflict)
	}
	return fmt.Errorf("%s: %w", entity, domain.ErrNotFound)
}
