package syncengine

import (
	"context"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/service"
)

// Per-kind dispatch: an Intent becomes arguments to exactly one service method.
//
// This file is deliberately dull. Every function here decodes, converts, and delegates — no
// branching on state, no fallback rules, no "if the client did not send X, guess". Anything
// clever added here would be business logic living outside a service, which is the one thing
// this package is not allowed to contain.

// parseOptionalID converts a nullable id string. Absent and null both yield nil; the FIELD
// MASK, not this function, decides whether nil means "clear it" or "leave it alone".
func parseOptionalID(field string, raw *string) (*domain.ID, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	id, err := domain.ParseID(field, *raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func parseOptionalTimeOfDay(field string, raw *string) (*domain.TimeOfDay, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	t, err := domain.ParseTimeOfDay(*raw)
	if err != nil {
		ve := &domain.ValidationError{}
		ve.Add(field, "invalid_time", "must be HH:MM")
		return nil, ve
	}
	return &t, nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (e *Engine) slotCreate(ctx context.Context, in Intent, v intentValues) error {
	dayID, err := parseOptionalID("day_id", v.DayID)
	if err != nil {
		return err
	}
	afterID, err := parseOptionalID("after_id", v.AfterID)
	if err != nil {
		return err
	}
	start, err := parseOptionalTimeOfDay("start_time", v.StartTime)
	if err != nil {
		return err
	}
	end, err := parseOptionalTimeOfDay("end_time", v.EndTime)
	if err != nil {
		return err
	}

	_, err = e.services.Slots.Create(ctx, in.TripID, in.ActorID, service.CreateSlotInput{
		// The client's chosen id is honoured (D4): it named the entity before the server saw
		// it, which is what lets its optimistic render survive the round trip unchanged.
		ID:        in.EntityID,
		DayID:     dayID,
		Kind:      domain.SlotKind(deref(v.Kind)),
		Title:     deref(v.Title),
		Notes:     deref(v.Notes),
		StartTime: start,
		EndTime:   end,
		// Intent-only. The server resolves it into a position key and logs the key (D62).
		AfterSlotID: afterID,
	})
	return err
}

func (e *Engine) slotEdit(ctx context.Context, in Intent, v intentValues) error {
	start, err := parseOptionalTimeOfDay("start_time", v.StartTime)
	if err != nil {
		return err
	}
	end, err := parseOptionalTimeOfDay("end_time", v.EndTime)
	if err != nil {
		return err
	}
	_, err = e.services.Slots.Update(ctx, in.TripID, in.ActorID, in.EntityID, service.UpdateSlotInput{
		Fields:    in.Fields,
		Kind:      domain.SlotKind(deref(v.Kind)),
		Title:     deref(v.Title),
		Notes:     deref(v.Notes),
		StartTime: start,
		EndTime:   end,
		Version:   v.Version,
	})
	return err
}

func (e *Engine) slotMove(ctx context.Context, in Intent, v intentValues) error {
	dayID, err := parseOptionalID("day_id", v.DayID)
	if err != nil {
		return err
	}
	afterID, err := parseOptionalID("after_id", v.AfterID)
	if err != nil {
		return err
	}
	return e.services.Slots.Move(ctx, in.TripID, in.ActorID, in.EntityID, dayID, afterID, v.Version)
}

func (e *Engine) slotSelectOption(ctx context.Context, in Intent, v intentValues) error {
	optionID, err := parseOptionalID("selected_option_id", v.SelectedOptionID)
	if err != nil {
		return err
	}
	return e.services.Slots.SetSelectedOption(ctx, in.TripID, in.ActorID, in.EntityID, optionID, v.Version)
}

func (e *Engine) slotSetStatus(ctx context.Context, in Intent, v intentValues) error {
	return e.services.Slots.SetStatus(ctx, in.TripID, in.ActorID, in.EntityID,
		domain.SlotStatus(deref(v.Status)), v.Version)
}

func (e *Engine) optionCreate(ctx context.Context, in Intent, v intentValues) error {
	slotID, err := parseOptionalID("slot_id", v.SlotID)
	if err != nil {
		return err
	}
	if slotID == nil {
		ve := &domain.ValidationError{}
		ve.Add("slot_id", "required", "an option must name the slot it belongs to")
		return ve
	}
	_, err = e.services.Options.Create(ctx, in.TripID, in.ActorID, *slotID, service.CreateOptionInput{
		ID:                 in.EntityID,
		Title:              deref(v.Title),
		Notes:              deref(v.Notes),
		ExternalURL:        deref(v.ExternalURL),
		EstimatedCostMinor: v.EstimatedCostMinor,
		Place: domain.Place{
			Name:       deref(v.PlaceName),
			Address:    deref(v.PlaceAddress),
			Lat:        v.PlaceLat,
			Lng:        v.PlaceLng,
			ProviderID: deref(v.PlaceProviderID),
		},
	})
	return err
}

func (e *Engine) optionEdit(ctx context.Context, in Intent, v intentValues) error {
	_, err := e.services.Options.Update(ctx, in.TripID, in.ActorID, in.EntityID, service.UpdateOptionInput{
		Fields:             in.Fields,
		Title:              deref(v.Title),
		Notes:              deref(v.Notes),
		ExternalURL:        deref(v.ExternalURL),
		EstimatedCostMinor: v.EstimatedCostMinor,
		Place: domain.Place{
			Name:       deref(v.PlaceName),
			Address:    deref(v.PlaceAddress),
			Lat:        v.PlaceLat,
			Lng:        v.PlaceLng,
			ProviderID: deref(v.PlaceProviderID),
		},
		Version: v.Version,
	})
	return err
}

// voteSet is the whole vote vocabulary.
//
// Note what is absent: no version, no create/delete distinction, no tombstone handling. A vote
// is a last-writer-wins register, so "changing your mind" is the same operation as "voting",
// and retraction is a null value rather than a deletion. It travels the identical path as
// every other operation — same sequencer, same transaction, same log.
func (e *Engine) voteSet(ctx context.Context, in Intent, v intentValues) error {
	slotID, err := parseOptionalID("slot_id", v.SlotID)
	if err != nil {
		return err
	}
	if slotID == nil {
		ve := &domain.ValidationError{}
		ve.Add("slot_id", "required", "a vote must name its slot")
		return ve
	}
	optionID, err := parseOptionalID("option_id", v.OptionID)
	if err != nil {
		return err
	}
	// A member may only cast their OWN vote. The service scopes the row to the acting user,
	// so a client cannot vote on someone else's behalf by naming a different user_id — the
	// user_id in the mask is descriptive of the resulting row, never an input.
	_, err = e.services.Votes.Cast(ctx, in.TripID, in.ActorID, *slotID, optionID)
	return err
}

// budgetSet is the whole budget-edit vocabulary: create and edit are one verb, because a write
// replaces the entry together with its complete split set (D44).
//
// Note what this function does NOT do, and could not. It does not merge, it does not fill in
// omitted fields from the stored entry, and it does not derive a split set. Every one of those
// would be a partial budget write wearing a disguise, and the field mask that reached this point
// has already been validated as total — so an intent arriving here is, by construction, a
// complete entry. The `values` object carrying every field is a consequence of that rule, not a
// verbosity to be optimised away later.
func (e *Engine) budgetSet(ctx context.Context, in Intent, v intentValues) error {
	optionID, err := parseOptionalID("slot_option_id", v.SlotOptionID)
	if err != nil {
		return err
	}
	paidBy, err := parseOptionalID("paid_by", v.PaidBy)
	if err != nil {
		return err
	}

	splits := make([]domain.BudgetSplit, 0)
	if v.Splits != nil {
		for _, s := range *v.Splits {
			userID, err := domain.ParseID("splits.user_id", s.UserID)
			if err != nil {
				return err
			}
			splits = append(splits, domain.BudgetSplit{UserID: userID, AmountMinor: s.AmountMinor})
		}
	}

	input := service.BudgetEntryInput{
		ID:           in.EntityID,
		Label:        deref(v.Label),
		Category:     domain.BudgetCategory(deref(v.Category)),
		AmountMinor:  deref(v.AmountMinor),
		SlotOptionID: optionID,
		PaidBy:       paidBy,
		IncurredOn:   v.IncurredOn,
		Splits:       splits,
		Version:      v.Version,
	}

	// A version distinguishes an edit from a create, and its ABSENCE is not a fallback to
	// create: the service refuses a versionless edit (D85), and inventing one here would
	// silently convert "I meant to edit entry X" into "overwrite entry X with whatever I sent",
	// which is the exact outcome the required version exists to prevent.
	if v.Version == nil {
		_, err := e.services.Budget.Create(ctx, in.TripID, in.ActorID, input)
		return err
	}
	_, err = e.services.Budget.Update(ctx, in.TripID, in.ActorID, in.EntityID, input)
	return err
}

func (e *Engine) dayCreate(ctx context.Context, in Intent, v intentValues) error {
	afterID, err := parseOptionalID("after_id", v.AfterID)
	if err != nil {
		return err
	}
	_, err = e.services.Days.Create(ctx, in.TripID, in.ActorID, service.CreateDayInput{
		ID:         in.EntityID,
		Date:       v.Date,
		Label:      deref(v.Label),
		AfterDayID: afterID,
	})
	return err
}

func (e *Engine) dayEdit(ctx context.Context, in Intent, v intentValues) error {
	_, err := e.services.Days.Update(ctx, in.TripID, in.ActorID, in.EntityID, service.UpdateDayInput{
		Fields:  in.Fields,
		Date:    v.Date,
		Label:   deref(v.Label),
		Version: v.Version,
	})
	return err
}

func (e *Engine) dayMove(ctx context.Context, in Intent, v intentValues) error {
	afterID, err := parseOptionalID("after_id", v.AfterID)
	if err != nil {
		return err
	}
	return e.services.Days.Move(ctx, in.TripID, in.ActorID, in.EntityID, afterID, v.Version)
}
