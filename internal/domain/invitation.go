package domain

import "time"

// Invitation grants a role on a trip to whoever redeems it.
//
// One type covers both invite modes because the lifecycle is identical and only the
// delivery channel differs:
//
//   - Email is set   -> a targeted invite sent to that address.
//   - Email is nil   -> a shareable link.
//
// Only the hash is stored, so a database leak does not yield usable invite links.
type Invitation struct {
	ID     ID
	TripID ID

	// Email is nil for link invites. Nullable rather than empty-string because here the
	// distinction is real: "no target address" is a different kind of invitation, not an
	// absent value.
	Email *string

	Role      Role
	TokenHash []byte
	CreatedBy ID

	// MaxUses is nil for unlimited (typical for link invites). Email invites default to 1.
	MaxUses  *int
	UseCount int

	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// IsLinkInvite reports whether this is a shareable link rather than a targeted invite.
func (i *Invitation) IsLinkInvite() bool { return i.Email == nil }

// IsRedeemable reports whether the invitation can still be accepted at time now.
func (i *Invitation) IsRedeemable(now time.Time) bool {
	if i.RevokedAt != nil || !now.Before(i.ExpiresAt) {
		return false
	}
	return i.MaxUses == nil || i.UseCount < *i.MaxUses
}

// Validate checks the invitation's invariants.
func (i *Invitation) Validate() error {
	ve := &ValidationError{}

	ve.AddIf(i.TripID == NilID, "trip_id", "required", "trip id is required")
	ve.AddIf(i.CreatedBy == NilID, "created_by", "required", "creator is required")

	// Mirrors the trip_invitations_role CHECK. Owner is excluded in both places:
	// ownership transfer is a separate operation with separate rules, and an invite link
	// must never be able to grant it.
	if i.Role != RoleEditor && i.Role != RoleViewer {
		ve.Add("role", "invalid_role", "invitation role must be editor or viewer")
	}

	if i.Email != nil {
		ValidateEmail(ve, "email", *i.Email)
	}

	ve.AddIf(i.MaxUses != nil && *i.MaxUses < 1, "max_uses", "out_of_range", "max uses must be at least 1")
	ve.AddIf(i.UseCount < 0, "use_count", "out_of_range", "use count must not be negative")
	ve.AddIf(len(i.TokenHash) != 32, "token_hash", "invalid_length", "token hash must be 32 bytes")
	ve.AddIf(i.ExpiresAt.IsZero(), "expires_at", "required", "expiry is required")

	return ve.OrNil()
}
