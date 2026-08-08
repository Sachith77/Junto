package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/pkg/secrets"
)

// MembershipService manages who belongs to a trip and how they get in.
type MembershipService struct {
	authz
	trips       domain.TripRepository
	users       domain.UserRepository
	invitations domain.InvitationRepository
	mailer      domain.EmailSender
	tx          domain.TxManager
	clock       domain.Clock
	cfg         MembershipConfig
	log         *slog.Logger
}

// MembershipConfig is the subset of configuration this service needs.
type MembershipConfig struct {
	InvitationTTL time.Duration
	WebBaseURL    string
}

// MembershipDeps collects MembershipService's dependencies.
type MembershipDeps struct {
	Members     domain.MembershipRepository
	Trips       domain.TripRepository
	Users       domain.UserRepository
	Invitations domain.InvitationRepository
	Mailer      domain.EmailSender
	Tx          domain.TxManager
	Clock       domain.Clock
	Config      MembershipConfig
	Logger      *slog.Logger
}

// NewMembershipService builds a MembershipService.
func NewMembershipService(deps MembershipDeps) *MembershipService {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Config.InvitationTTL == 0 {
		deps.Config.InvitationTTL = 7 * 24 * time.Hour
	}
	return &MembershipService{
		authz:       authz{members: deps.Members},
		trips:       deps.Trips,
		users:       deps.Users,
		invitations: deps.Invitations,
		mailer:      deps.Mailer,
		tx:          deps.Tx,
		clock:       deps.Clock,
		cfg:         deps.Config,
		log:         deps.Logger,
	}
}

// ListMembers returns a trip's roster. Membership itself is the read gate.
func (s *MembershipService) ListMembers(ctx context.Context, tripID, userID domain.ID) ([]*domain.Member, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	return s.members.List(ctx, tripID)
}

// UpdateRole changes a member's role.
//
// Ownership cannot be granted or revoked through this path. Transferring ownership is a
// distinct operation with distinct rules (CapTransferOwnership exists for it) and is not
// implemented in this slice; blocking it here rather than leaving it silently reachable is
// what keeps that boundary honest until the real flow exists.
func (s *MembershipService) UpdateRole(ctx context.Context, tripID, actorUserID, targetUserID domain.ID, newRole domain.Role, expectedVersion int) (*domain.Member, error) {
	if _, err := s.require(ctx, tripID, actorUserID, domain.CapManageMembers); err != nil {
		return nil, err
	}

	if newRole != domain.RoleEditor && newRole != domain.RoleViewer {
		ve := &domain.ValidationError{}
		ve.Add("role", "invalid_role", "role must be editor or viewer")
		return nil, ve
	}

	target, err := s.members.Get(ctx, tripID, targetUserID)
	if err != nil {
		return nil, err
	}
	if target.Role == domain.RoleOwner {
		return nil, fmt.Errorf("%w: ownership is transferred, not edited", domain.ErrForbidden)
	}

	target.Role = newRole
	target.Version = expectedVersion
	if err := s.members.UpdateRole(ctx, target); err != nil {
		return nil, err
	}
	return target, nil
}

// RemoveMember removes a member from a trip. The owner cannot be removed through this path —
// there is no ownership transfer flow yet, and removing the owner would leave the trip with
// no one holding owner-only capabilities.
func (s *MembershipService) RemoveMember(ctx context.Context, tripID, actorUserID, targetUserID domain.ID) error {
	if _, err := s.require(ctx, tripID, actorUserID, domain.CapManageMembers); err != nil {
		return err
	}

	target, err := s.members.Get(ctx, tripID, targetUserID)
	if err != nil {
		return err
	}
	if target.Role == domain.RoleOwner {
		return fmt.Errorf("%w: the owner cannot be removed", domain.ErrForbidden)
	}
	return s.members.Remove(ctx, tripID, targetUserID, s.clock.Now())
}

// CreateInvitationInput is the input to CreateInvitation.
type CreateInvitationInput struct {
	// Email is nil for a shareable link invite.
	Email *string
	Role  domain.Role
	// MaxUses is nil for unlimited (typical for link invites). A targeted email invite that
	// leaves this nil is deliberately allowed too — some trips forward one email invite
	// around a family group on purpose — but the handler defaults it to 1 for the common case.
	MaxUses *int
}

// CreateInvitation issues an invitation and, for a targeted email invite, sends it.
func (s *MembershipService) CreateInvitation(ctx context.Context, tripID, userID domain.ID, in CreateInvitationInput) (*domain.Invitation, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapInviteMembers)
	if err != nil {
		return nil, err
	}

	token, err := secrets.New()
	if err != nil {
		return nil, fmt.Errorf("generating invitation token: %w", err)
	}

	inv := &domain.Invitation{
		ID:        domain.NewID(),
		TripID:    tripID,
		Email:     in.Email,
		Role:      in.Role,
		TokenHash: token.Hash,
		CreatedBy: actor.UserID,
		MaxUses:   in.MaxUses,
		ExpiresAt: s.clock.Now().Add(s.cfg.InvitationTTL),
	}
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	if err := s.invitations.Create(ctx, inv); err != nil {
		return nil, err
	}

	if inv.Email != nil {
		s.sendInvitationEmail(ctx, tripID, *inv.Email, token.Raw)
	}
	return inv, nil
}

// ListInvitations returns a trip's outstanding invitations.
func (s *MembershipService) ListInvitations(ctx context.Context, tripID, userID domain.ID) ([]*domain.Invitation, error) {
	if _, err := s.require(ctx, tripID, userID, domain.CapInviteMembers); err != nil {
		return nil, err
	}
	return s.invitations.ListForTrip(ctx, tripID)
}

// RevokeInvitation ends an invitation early.
func (s *MembershipService) RevokeInvitation(ctx context.Context, tripID, userID, invitationID domain.ID) error {
	if _, err := s.require(ctx, tripID, userID, domain.CapInviteMembers); err != nil {
		return err
	}
	return s.invitations.Revoke(ctx, invitationID, s.clock.Now())
}

// RedeemInvitation consumes an invitation link and adds the caller to the trip.
//
// The atomic guard lives in InvitationRepository.IncrementUseCount, which checks every
// validity condition (not revoked, not expired, uses remaining) in the same statement that
// consumes a use. This method does NOT pre-check redeemability — that would reopen exactly
// the read-then-write race the repository closes.
func (s *MembershipService) RedeemInvitation(ctx context.Context, userID domain.ID, rawToken string) (*domain.Trip, error) {
	inv, err := s.invitations.GetByHash(ctx, secrets.Hash(rawToken))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrTokenInvalid
		}
		return nil, err
	}

	// A targeted email invite may only be redeemed by the account it named. Without this, an
	// invite link forwarded or leaked would let anyone with the raw token join under someone
	// else's intended role — the email check is what makes "targeted" mean something.
	if inv.Email != nil {
		user, err := s.users.GetByID(ctx, userID)
		if err != nil {
			return nil, err
		}
		if domain.NormalizeEmail(user.Email) != domain.NormalizeEmail(*inv.Email) {
			// Same opaque failure as an invalid token: confirming "this link was for someone
			// else" discloses that the target address has an account.
			return nil, domain.ErrTokenInvalid
		}
	}

	var trip *domain.Trip
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.invitations.IncrementUseCount(ctx, inv.ID); err != nil {
			return err
		}

		_, getErr := s.members.Get(ctx, inv.TripID, userID)
		switch {
		case getErr == nil:
			// Already a member. Idempotent success: redeeming a link twice (a double click,
			// a stale tab) should not error just because the first click already worked. The
			// use was still counted above, which is the conservative side to fail on for a
			// limited-use link.
		case errors.Is(getErr, domain.ErrNotFound):
			if err := s.members.Add(ctx, &domain.Member{
				ID:        domain.NewID(),
				TripID:    inv.TripID,
				UserID:    userID,
				Role:      inv.Role,
				InvitedBy: &inv.CreatedBy,
			}); err != nil {
				return err
			}
		default:
			return getErr
		}

		t, err := s.trips.GetByID(ctx, inv.TripID)
		if err != nil {
			return err
		}
		trip = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	return trip, nil
}

func (s *MembershipService) sendInvitationEmail(ctx context.Context, tripID domain.ID, to, rawToken string) {
	trip, err := s.trips.GetByID(ctx, tripID)
	if err != nil {
		s.log.ErrorContext(ctx, "loading trip for invitation email failed", "error", err)
		return
	}
	link := fmt.Sprintf("%s/invitations/accept?token=%s", s.cfg.WebBaseURL, rawToken)
	err = s.mailer.Send(ctx, domain.EmailMessage{
		To:      to,
		Subject: fmt.Sprintf("You're invited to plan %q on Junto", trip.Name),
		TextBody: fmt.Sprintf(
			"You have been invited to help plan \"%s\" on Junto.\n\nJoin here:\n\n%s\n\n"+
				"This link expires in %s.\n", trip.Name, link, s.cfg.InvitationTTL),
	})
	if err != nil {
		s.log.ErrorContext(ctx, "sending invitation email failed", "error", err)
	}
}
