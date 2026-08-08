package service

import (
	"context"

	"github.com/junto/junto/internal/domain"
)

// CommentService manages flat, per-slot discussion.
//
// Append-only, the same treatment as attachments (D46/D84), decided before this file was
// written: there is no Update, because there is no comment.edit.v1 — post a new one, delete
// the old one.
type CommentService struct {
	authz
	oplog
	comments domain.CommentRepository
	slots    domain.SlotRepository
	clock    domain.Clock
}

// CommentDeps collects CommentService's dependencies.
type CommentDeps struct {
	Comments domain.CommentRepository
	Slots    domain.SlotRepository
	Members  domain.MembershipRepository
	Trips    domain.TripRepository
	Ops      domain.OpLogRepository
	Tx       domain.TxManager
	Pub      domain.OpPublisher
	Clock    domain.Clock
}

// NewCommentService builds a CommentService.
func NewCommentService(deps CommentDeps) *CommentService {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	return &CommentService{
		authz:    authz{members: deps.Members},
		oplog:    newOplog(deps.Trips, deps.Ops, deps.Tx, deps.Pub),
		comments: deps.Comments,
		slots:    deps.Slots,
		clock:    deps.Clock,
	}
}

// ListForSlot returns a slot's comments in chronological order.
func (s *CommentService) ListForSlot(ctx context.Context, tripID, userID, slotID domain.ID) ([]*domain.Comment, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	if err := s.checkSlotInTrip(ctx, tripID, slotID); err != nil {
		return nil, err
	}
	return s.comments.ListForSlot(ctx, slotID)
}

// CreateCommentInput is the input to Create.
type CreateCommentInput struct {
	Body string
	// ID lets a client name the comment before the server has seen it (D4).
	ID domain.ID
}

// Create posts a comment on a slot. The caller is always the author — there is no way to post
// on someone else's behalf.
func (s *CommentService) Create(ctx context.Context, tripID, userID, slotID domain.ID, in CreateCommentInput) (*domain.Comment, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapComment)
	if err != nil {
		return nil, err
	}

	id := in.ID
	if id == domain.NilID {
		id = domain.NewID()
	}
	author := actor.UserID
	comment := &domain.Comment{
		ID: id, SlotID: slotID, TripID: tripID, Body: in.Body, AuthorID: &author,
	}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		if err := s.checkSlotInTrip(ctx, tripID, slotID); err != nil {
			return err
		}
		if err := comment.Validate(); err != nil {
			return err
		}
		if err := s.comments.Create(ctx, comment); err != nil {
			return err
		}
		return rec.comment(ctx, domain.OpCommentCreate, comment)
	})
	if err != nil {
		return nil, err
	}
	return comment, nil
}

// Delete removes the caller's OWN comment.
//
// Author-only — the one deliberate departure from every other delete path in this codebase
// (slots, options and attachments are capability-gated only, because they are shared planning
// artifacts an editor may prune). A comment is a personal utterance with no precedent to copy
// here, so the more conservative reading was chosen: the author can delete their own comment,
// nobody else can, not even the trip owner. If the author's account is gone (author_id is
// NULL, D18 — history outlives accounts), nobody can delete it any longer; that is an accepted
// consequence of the same choice, not a separate gap.
func (s *CommentService) Delete(ctx context.Context, tripID, userID, commentID domain.ID) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapComment)
	if err != nil {
		return err
	}

	now := s.clock.Now()
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, commentID)
		if err != nil {
			return err
		}
		if existing.AuthorID == nil || *existing.AuthorID != actor.UserID {
			return domain.ErrForbidden
		}
		if err := s.comments.SoftDelete(ctx, commentID, now); err != nil {
			return err
		}

		tombstone := *existing
		tombstone.DeletedAt = &now
		tombstone.UpdatedAt = now
		return rec.comment(ctx, domain.OpCommentDelete, &tombstone, domain.FieldDeletedAt)
	})
	return err
}

func (s *CommentService) getInTrip(ctx context.Context, tripID, commentID domain.ID) (*domain.Comment, error) {
	comment, err := s.comments.GetByID(ctx, commentID)
	if err != nil {
		return nil, err
	}
	if err := checkTrip(comment.TripID, tripID); err != nil {
		return nil, err
	}
	return comment, nil
}

func (s *CommentService) checkSlotInTrip(ctx context.Context, tripID, slotID domain.ID) error {
	slot, err := s.slots.GetByID(ctx, slotID)
	if err != nil {
		return err
	}
	return checkTrip(slot.TripID, tripID)
}
