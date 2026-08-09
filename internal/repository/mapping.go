package repository

import (
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// Translation between generated row structs and domain types.
//
// This file is the boilerplate that was promised up front. It exists so that no sqlcgen
// type ever escapes this package, which is what makes "internal/service depends only on
// domain interfaces" a fact rather than an aspiration. It is dull on purpose: every function
// here should be obviously correct at a glance, because subtle logic hidden in a mapper is
// the hardest kind of bug to find.
//
// The only genuinely interesting conversions are time-of-day and versions; both are
// commented where they happen.

// --- users ---

func toDomainUser(row sqlcgen.User) *domain.User {
	return &domain.User{
		ID:              row.ID,
		Email:           row.Email,
		PasswordHash:    row.PasswordHash,
		DisplayName:     row.DisplayName,
		EmailVerifiedAt: row.EmailVerifiedAt,
		Version:         int(row.Version),
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		DeletedAt:       row.DeletedAt,
	}
}

// --- sessions and tokens ---

func toDomainSession(row sqlcgen.AuthSession) *domain.AuthSession {
	s := &domain.AuthSession{
		ID:         row.ID,
		UserID:     row.UserID,
		UserAgent:  row.UserAgent,
		IP:         row.Ip,
		CreatedAt:  row.CreatedAt,
		LastUsedAt: row.LastUsedAt,
		ExpiresAt:  row.ExpiresAt,
		RevokedAt:  row.RevokedAt,
	}
	if row.RevokedReason != nil {
		s.RevokedReason = *row.RevokedReason
	}
	return s
}

func toDomainRefreshToken(row sqlcgen.RefreshToken) *domain.RefreshToken {
	return &domain.RefreshToken{
		ID:         row.ID,
		SessionID:  row.SessionID,
		TokenHash:  row.TokenHash,
		IssuedAt:   row.IssuedAt,
		ExpiresAt:  row.ExpiresAt,
		UsedAt:     row.UsedAt,
		ReplacedBy: row.ReplacedBy,
	}
}

func toDomainUserToken(row sqlcgen.UserToken) *domain.UserToken {
	return &domain.UserToken{
		ID:         row.ID,
		UserID:     row.UserID,
		Purpose:    domain.TokenPurpose(row.Purpose),
		TokenHash:  row.TokenHash,
		ExpiresAt:  row.ExpiresAt,
		ConsumedAt: row.ConsumedAt,
		CreatedAt:  row.CreatedAt,
	}
}

// --- trips, members, invitations ---

func toDomainTrip(row sqlcgen.Trip) *domain.Trip {
	return &domain.Trip{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		StartDate:   row.StartDate,
		EndDate:     row.EndDate,
		TimeZone:    row.TimeZone,
		Version:     int(row.Version),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		DeletedAt:   row.DeletedAt,
	}
}

func toDomainMember(row sqlcgen.TripMember) *domain.Member {
	return &domain.Member{
		ID:        row.ID,
		TripID:    row.TripID,
		UserID:    row.UserID,
		Role:      domain.Role(row.Role),
		InvitedBy: row.InvitedBy,
		JoinedAt:  row.JoinedAt,
		Version:   int(row.Version),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		DeletedAt: row.DeletedAt,
	}
}

func toDomainMemberProfile(row sqlcgen.ListMemberProfilesRow) *domain.MemberProfile {
	return &domain.MemberProfile{
		Member: domain.Member{
			ID:        row.ID,
			TripID:    row.TripID,
			UserID:    row.UserID,
			Role:      domain.Role(row.Role),
			InvitedBy: row.InvitedBy,
			JoinedAt:  row.JoinedAt,
			Version:   int(row.Version),
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
			DeletedAt: row.DeletedAt,
		},
		DisplayName: row.DisplayName,
	}
}

func toDomainInvitation(row sqlcgen.TripInvitation) *domain.Invitation {
	inv := &domain.Invitation{
		ID:        row.ID,
		TripID:    row.TripID,
		Email:     row.Email,
		Role:      domain.Role(row.Role),
		TokenHash: row.TokenHash,
		CreatedBy: row.CreatedBy,
		UseCount:  int(row.UseCount),
		ExpiresAt: row.ExpiresAt,
		RevokedAt: row.RevokedAt,
		CreatedAt: row.CreatedAt,
	}
	if row.MaxUses != nil {
		n := int(*row.MaxUses)
		inv.MaxUses = &n
	}
	return inv
}

// --- days and items ---

func toDomainDay(row sqlcgen.Day) *domain.Day {
	return &domain.Day{
		ID:        row.ID,
		TripID:    row.TripID,
		Date:      row.Date,
		Label:     row.Label,
		Position:  row.Position,
		Version:   int(row.Version),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		DeletedAt: row.DeletedAt,
	}
}

func toDomainSlot(row sqlcgen.Slot) *domain.Slot {
	return &domain.Slot{
		ID:               row.ID,
		TripID:           row.TripID,
		DayID:            row.DayID,
		Kind:             domain.SlotKind(row.Kind),
		Title:            row.Title,
		Notes:            row.Notes,
		StartTime:        toDomainTimeOfDay(row.StartTime),
		EndTime:          toDomainTimeOfDay(row.EndTime),
		Position:         row.Position,
		SelectedOptionID: row.SelectedOptionID,
		Status:           domain.SlotStatus(row.Status),
		StatusChangedAt:  row.StatusChangedAt,
		StatusChangedBy:  row.StatusChangedBy,
		CreatedBy:        row.CreatedBy,
		Version:          int(row.Version),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		DeletedAt:        row.DeletedAt,
	}
}

func toDomainSlots(rows []sqlcgen.Slot) []*domain.Slot {
	out := make([]*domain.Slot, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainSlot(row))
	}
	return out
}

func toDomainSlotOption(row sqlcgen.SlotOption) *domain.SlotOption {
	return &domain.SlotOption{
		ID:                 row.ID,
		SlotID:             row.SlotID,
		TripID:             row.TripID,
		Title:              row.Title,
		Notes:              row.Notes,
		ExternalURL:        row.ExternalUrl,
		EstimatedCostMinor: row.EstimatedCostMinor,
		Place: domain.Place{
			Name:       row.PlaceName,
			Address:    row.PlaceAddress,
			Lat:        row.PlaceLat,
			Lng:        row.PlaceLng,
			ProviderID: row.PlaceProviderID,
		},
		ProposedBy: row.ProposedBy,
		Version:    int(row.Version),
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
		DeletedAt:  row.DeletedAt,
	}
}

func toDomainSlotOptions(rows []sqlcgen.SlotOption) []*domain.SlotOption {
	out := make([]*domain.SlotOption, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainSlotOption(row))
	}
	return out
}

func toDomainVote(row sqlcgen.OptionVote) *domain.Vote {
	return &domain.Vote{
		ID:        row.ID,
		SlotID:    row.SlotID,
		TripID:    row.TripID,
		UserID:    row.UserID,
		OptionID:  row.OptionID,
		Version:   int(row.Version),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func toDomainVotes(rows []sqlcgen.OptionVote) []*domain.Vote {
	out := make([]*domain.Vote, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainVote(row))
	}
	return out
}

// --- budget ---
//
// A budget entry is mapped WITHOUT its splits, and the split set is attached by the repository
// after a second query. That is deliberate: the two are one atomic unit to a writer (D44), but
// they are two tables to a reader, and pretending otherwise here would mean either a join whose
// duplicated entry columns have to be de-duplicated in Go, or a mapper that silently returns an
// entry with an empty split set that looks identical to an entry nobody has split yet.

func toDomainBudgetEntry(row sqlcgen.BudgetEntry) *domain.BudgetEntry {
	return &domain.BudgetEntry{
		ID:           row.ID,
		TripID:       row.TripID,
		SlotOptionID: row.SlotOptionID,
		Label:        row.Label,
		Category:     domain.BudgetCategory(row.Category),
		AmountMinor:  row.AmountMinor,
		PaidBy:       row.PaidBy,
		IncurredOn:   row.IncurredOn,
		CreatedBy:    row.CreatedBy,
		Version:      int(row.Version),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		DeletedAt:    row.DeletedAt,
	}
}

func toDomainBudgetSplit(row sqlcgen.BudgetSplit) domain.BudgetSplit {
	return domain.BudgetSplit{
		ID:          row.ID,
		UserID:      row.UserID,
		AmountMinor: row.AmountMinor,
		CreatedAt:   row.CreatedAt,
	}
}

func toDomainBudgetSplits(rows []sqlcgen.BudgetSplit) []domain.BudgetSplit {
	out := make([]domain.BudgetSplit, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainBudgetSplit(row))
	}
	return out
}

// --- attachments ---

func toDomainAttachment(row sqlcgen.Attachment) *domain.Attachment {
	return &domain.Attachment{
		ID:             row.ID,
		TripID:         row.TripID,
		SlotOptionID:   row.SlotOptionID,
		BudgetEntryID:  row.BudgetEntryID,
		SlotID:         row.SlotID,
		Kind:           domain.AttachmentKind(row.Kind),
		Status:         domain.AttachmentStatus(row.Status),
		StorageKey:     row.StorageKey,
		ExternalURL:    row.ExternalUrl,
		ContentType:    row.ContentType,
		SizeBytes:      row.SizeBytes,
		ChecksumSHA256: row.ChecksumSha256,
		OriginalName:   row.OriginalName,
		UploadedBy:     row.UploadedBy,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		DeletedAt:      row.DeletedAt,
	}
}

func toDomainAttachments(rows []sqlcgen.Attachment) []*domain.Attachment {
	out := make([]*domain.Attachment, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainAttachment(row))
	}
	return out
}

func toDomainComment(row sqlcgen.Comment) *domain.Comment {
	return &domain.Comment{
		ID:        row.ID,
		SlotID:    row.SlotID,
		TripID:    row.TripID,
		Body:      row.Body,
		AuthorID:  row.AuthorID,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		DeletedAt: row.DeletedAt,
	}
}

func toDomainComments(rows []sqlcgen.Comment) []*domain.Comment {
	out := make([]*domain.Comment, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainComment(row))
	}
	return out
}

// --- operation log ---
//
// The one mapping in this file with no field conversion at all: trip_ops is stored in the
// shape the domain already uses, because a log entry has no behaviour to model — it is a
// record of something that already happened.

func toDomainOp(row sqlcgen.TripOp) *domain.Op {
	return &domain.Op{
		ID:         row.ID,
		TripID:     row.TripID,
		Seq:        row.Seq,
		ActorID:    row.ActorID,
		Kind:       domain.OpKind(row.Kind),
		EntityID:   row.EntityID,
		Fields:     domain.FieldMask(row.Fields),
		Payload:    row.Payload,
		ClientOpID: row.ClientOpID,
		CauseOpID:  row.CauseOpID,
		CreatedAt:  row.CreatedAt,
	}
}

// --- time of day ---
//
// Postgres `time` arrives as microseconds since midnight. The domain deliberately models a
// wall-clock time as {Hour, Minute} rather than a duration or a time.Time, so this is the
// one place the two representations meet.

const microsPerSecond = 1_000_000

func toDomainTimeOfDay(t pgtype.Time) *domain.TimeOfDay {
	if !t.Valid {
		return nil
	}
	totalSeconds := t.Microseconds / microsPerSecond
	return &domain.TimeOfDay{
		Hour:   int(totalSeconds / 3600),
		Minute: int((totalSeconds % 3600) / 60),
	}
}

func fromDomainTimeOfDay(t *domain.TimeOfDay) pgtype.Time {
	if t == nil {
		return pgtype.Time{Valid: false}
	}
	seconds := int64(t.Hour)*3600 + int64(t.Minute)*60
	return pgtype.Time{Microseconds: seconds * microsPerSecond, Valid: true}
}

// --- versions ---
//
// Version columns are `integer` in Postgres, so sqlc generates int32 while the domain uses
// a plain int. Converting through one named function rather than scattering int32() casts
// keeps the narrowing visible and gives a single place to add a guard if versions ever get
// close to overflowing (at one increment per write, they will not).

func versionArg(v int) int32 { return int32(v) }
