package service

import (
	"context"
	"fmt"

	"github.com/junto/junto/internal/domain"
)

// authz resolves the authenticated caller into a domain.Actor for a trip.
//
// Embedded (not called via a package-level function) so every planning service shares one
// implementation and one failure mode: a caller who is not a member gets ErrNotFound, not
// ErrForbidden. That is deliberate — confirming a trip exists to someone with no access to
// it is itself a disclosure, the same reasoning already applied to session revocation in the
// auth service.
type authz struct {
	members domain.MembershipRepository
}

// actor loads the caller's membership and projects it into an Actor. errors.Is(err,
// ErrNotFound) covers both "no such trip" and "not a member of this trip" — from outside,
// those must be indistinguishable.
func (a authz) actor(ctx context.Context, tripID, userID domain.ID) (domain.Actor, error) {
	m, err := a.members.Get(ctx, tripID, userID)
	if err != nil {
		return domain.Actor{}, err
	}
	return m.Actor(), nil
}

// require resolves the actor and checks one capability in a single call, which is the shape
// every write path in this package needs.
func (a authz) require(ctx context.Context, tripID, userID domain.ID, cap domain.Capability) (domain.Actor, error) {
	actor, err := a.actor(ctx, tripID, userID)
	if err != nil {
		return domain.Actor{}, err
	}
	if err := actor.Authorize(cap); err != nil {
		return domain.Actor{}, err
	}
	return actor, nil
}

// errWrongTrip is returned when a resource loaded by ID does not belong to the trip named in
// the request.
//
// This is the defense-in-depth check every nested-resource method in this package performs
// after loading: the URL says trip A, capability was checked against trip A's membership, but
// the slot/day/option id in the URL might belong to trip B. Without this check, a member of
// trip A who can guess or observe a trip-B resource id could act on it while being authorized
// against the wrong trip entirely.
var errWrongTrip = fmt.Errorf("%w: resource does not belong to this trip", domain.ErrNotFound)

func checkTrip(resourceTripID, wantTripID domain.ID) error {
	if resourceTripID != wantTripID {
		return errWrongTrip
	}
	return nil
}
