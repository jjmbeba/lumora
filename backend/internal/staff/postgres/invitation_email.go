package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jjmbeba/lumora/backend/internal/staff"
	"github.com/jjmbeba/lumora/backend/internal/staff/postgres/dbgen"
)

type InvitationEmailOutboxStore struct {
	db *sql.DB
}

var _ staff.InvitationEmailStore = (*InvitationEmailOutboxStore)(nil)

func NewInvitationEmailOutboxStore(db *sql.DB) (*InvitationEmailOutboxStore, error) {
	if db == nil {
		return nil, errors.New("staff invitation email database pool is required")
	}
	return &InvitationEmailOutboxStore{db: db}, nil
}

func (s *InvitationEmailOutboxStore) ClaimNextInvitationEmail(ctx context.Context, lease time.Duration) (staff.InvitationEmailJob, bool, error) {
	if lease < time.Microsecond {
		return staff.InvitationEmailJob{}, false, errors.New("staff invitation email lease must be at least one microsecond")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return staff.InvitationEmailJob{}, false, fmt.Errorf("begin staff invitation email claim: %w", err)
	}
	defer tx.Rollback()
	queries := dbgen.New(tx)
	if err := queries.FailExpiredStaffInvitationEmailJobs(ctx); err != nil {
		return staff.InvitationEmailJob{}, false, fmt.Errorf("fail expired staff invitation email jobs: %w", err)
	}
	if err := queries.RevokeExpiredStaffInvitations(ctx); err != nil {
		return staff.InvitationEmailJob{}, false, fmt.Errorf("revoke expired staff invitations: %w", err)
	}
	row, err := queries.ClaimNextStaffInvitationEmail(ctx, lease.Microseconds())
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return staff.InvitationEmailJob{}, false, fmt.Errorf("commit staff invitation email cleanup: %w", err)
		}
		return staff.InvitationEmailJob{}, false, nil
	}
	if err != nil {
		return staff.InvitationEmailJob{}, false, fmt.Errorf("claim staff invitation email: %w", err)
	}
	if !row.StaffInvitationID.Valid {
		return staff.InvitationEmailJob{}, false, errors.New("claimed staff invitation email has no invitation ID")
	}
	job := staff.InvitationEmailJob{
		ID: int(row.ID), InvitationID: int(row.StaffInvitationID.Int32),
		Recipient: row.RecipientEmail, EventKey: row.EventKey, AttemptCount: int(row.AttemptCount),
	}
	if err := tx.Commit(); err != nil {
		return staff.InvitationEmailJob{}, false, fmt.Errorf("commit staff invitation email claim: %w", err)
	}
	return job, true, nil
}

func (s *InvitationEmailOutboxStore) LoadInvitationEmail(ctx context.Context, job staff.InvitationEmailJob) (staff.InvitationEmailDelivery, error) {
	if job.ID <= 0 || job.InvitationID <= 0 || job.AttemptCount <= 0 {
		return staff.InvitationEmailDelivery{}, errors.New("staff invitation email claim is invalid")
	}
	row, err := dbgen.New(s.db).LoadStaffInvitationEmail(ctx, dbgen.LoadStaffInvitationEmailParams{
		ID: int32(job.ID), AttemptCount: int32(job.AttemptCount),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return staff.InvitationEmailDelivery{}, staff.ErrInvitationEmailClaimLost
	}
	if err != nil {
		return staff.InvitationEmailDelivery{}, fmt.Errorf("load staff invitation email %d: %w", job.ID, err)
	}
	if !row.TokenKeyVersion.Valid || row.TokenKeyVersion.Int16 <= 0 || len(row.TokenCiphertext) == 0 {
		return staff.InvitationEmailDelivery{}, errors.New("staff invitation email token ciphertext is unavailable")
	}
	return staff.InvitationEmailDelivery{
		Email: row.Email, TokenCiphertext: append([]byte(nil), row.TokenCiphertext...),
		TokenKeyVersion: row.TokenKeyVersion.Int16, ExpiresAt: row.ExpiresAt,
	}, nil
}

func (s *InvitationEmailOutboxStore) MarkInvitationEmailSent(ctx context.Context, job staff.InvitationEmailJob) error {
	if job.ID <= 0 || job.InvitationID <= 0 || job.AttemptCount <= 0 {
		return errors.New("staff invitation email claim is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark staff invitation email sent: %w", err)
	}
	defer tx.Rollback()
	queries := dbgen.New(tx)
	invitationID, err := queries.MarkStaffInvitationEmailSent(ctx, dbgen.MarkStaffInvitationEmailSentParams{
		ID: int32(job.ID), AttemptCount: int32(job.AttemptCount),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return staff.ErrInvitationEmailClaimLost
	}
	if err != nil {
		return fmt.Errorf("mark staff invitation email %d sent: %w", job.ID, err)
	}
	if !invitationID.Valid {
		return errors.New("sent staff invitation email has no invitation ID")
	}
	if err := queries.ClearStaffInvitationTokenCiphertext(ctx, invitationID.Int32); err != nil {
		return fmt.Errorf("clear sent staff invitation token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sent staff invitation email: %w", err)
	}
	return nil
}

func (s *InvitationEmailOutboxStore) RetryInvitationEmail(ctx context.Context, job staff.InvitationEmailJob, retryAt time.Time, terminal bool) error {
	if job.ID <= 0 || job.AttemptCount <= 0 {
		return errors.New("staff invitation email claim is invalid")
	}
	_, err := dbgen.New(s.db).RetryStaffInvitationEmail(ctx, dbgen.RetryStaffInvitationEmailParams{
		Terminal: terminal, NextAttemptAt: retryAt, ID: int32(job.ID), AttemptCount: int32(job.AttemptCount),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return staff.ErrInvitationEmailClaimLost
	}
	if err != nil {
		return fmt.Errorf("schedule staff invitation email retry for %d: %w", job.ID, err)
	}
	return nil
}
