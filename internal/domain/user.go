package domain

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// User is an identity. This is CORE: it has nothing to do with trips and a second domain
// module would reuse it unchanged.
type User struct {
	ID           ID
	Email        string // as the user typed it; uniqueness is folded, storage is not
	PasswordHash string
	DisplayName  string

	// EmailVerifiedAt is nil until verification completes. Nullable rather than a bool
	// because "when" is genuinely useful (support, audit) and a bool throws it away.
	EmailVerifiedAt *time.Time

	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// IsEmailVerified reports whether verification has completed.
func (u *User) IsEmailVerified() bool { return u.EmailVerifiedAt != nil }

// IsDeleted reports whether the user is soft-deleted.
func (u *User) IsDeleted() bool { return u.DeletedAt != nil }

// NormalizeEmail produces the canonical form used for uniqueness and lookup.
//
// This must stay in exact agreement with the users_email_lower_uq index, which is built on
// lower(email). If the two ever disagree, duplicate accounts become possible: the
// application would look up one form while the database enforces uniqueness on another.
// That coupling is the reason this lives in the domain and not in a handler.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail checks a single email address, appending to ve under the given field name.
func ValidateEmail(ve *ValidationError, field, email string) {
	trimmed := strings.TrimSpace(email)
	switch {
	case trimmed == "":
		ve.Add(field, "required", "email is required")
	case len(trimmed) > 320:
		ve.Add(field, "too_long", "email must be at most 320 characters")
	default:
		// net/mail accepts display-name forms like `Bob <b@x.com>` which we do not want
		// as an identity, so require the parse to round-trip to the bare address.
		addr, err := mail.ParseAddress(trimmed)
		if err != nil || addr.Address != trimmed || !strings.Contains(trimmed, "@") {
			ve.Add(field, "invalid_format", "must be a valid email address")
		}
	}
}

// MinPasswordLength is a floor, not a policy.
//
// Length is the only password rule enforced. Composition rules (a digit, a symbol, mixed
// case) measurably push users toward predictable substitutions without improving entropy,
// and NIST SP 800-63B recommends against them. Strength comes from Argon2id plus rate
// limiting on the login endpoint, not from character-class theatre.
const MinPasswordLength = 12

// MaxPasswordLength caps input before it reaches the hasher. Argon2id has no bcrypt-style
// 72-byte truncation problem, but an unbounded password is an unbounded amount of memory-hard
// work per request — i.e. a free denial-of-service primitive.
const MaxPasswordLength = 1024

// ValidatePassword checks a plaintext password.
func ValidatePassword(ve *ValidationError, field, password string) {
	switch {
	case password == "":
		ve.Add(field, "required", "password is required")
	case utf8.RuneCountInString(password) < MinPasswordLength:
		ve.Add(field, "too_short", "password must be at least 12 characters")
	case len(password) > MaxPasswordLength:
		ve.Add(field, "too_long", "password must be at most 1024 bytes")
	}
}

// ValidateDisplayName checks a display name.
func ValidateDisplayName(ve *ValidationError, field, name string) {
	trimmed := strings.TrimSpace(name)
	n := utf8.RuneCountInString(trimmed)
	switch {
	case trimmed == "":
		ve.Add(field, "required", "display name is required")
	case n > 100:
		ve.Add(field, "too_long", "display name must be at most 100 characters")
	}
}

// Validate checks the persisted shape of a user. Plaintext passwords are validated
// separately, at the point of entry, because they never reach this struct.
func (u *User) Validate() error {
	ve := &ValidationError{}
	ValidateEmail(ve, "email", u.Email)
	ValidateDisplayName(ve, "display_name", u.DisplayName)
	ve.AddIf(u.PasswordHash == "", "password_hash", "required", "password hash is required")
	return ve.OrNil()
}
