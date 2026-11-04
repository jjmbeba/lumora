package ticket

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTicketEmailWorkerSendsAndMarksJobSent(t *testing.T) {
	store := &fakeTicketEmailStore{email: TicketEmail{To: "guest@example.com", Tickets: []TicketEmailItem{{SeatLabel: "A1", Credential: "secret"}}}}
	sender := &fakeTicketEmailSender{}
	worker := newTestTicketEmailWorker(t, store, sender, 5)

	if err := worker.process(context.Background(), TicketEmailJob{ID: 3, AttemptCount: 1}); err != nil {
		t.Fatalf("process() error = %v", err)
	}
	if sender.calls != 1 || sender.lastEmail.To != "guest@example.com" {
		t.Errorf("sender calls/email = %d/%+v, want one send to guest@example.com", sender.calls, sender.lastEmail)
	}
	if store.markSentCalls != 1 || store.retryCalls != 0 {
		t.Errorf("store completion calls = sent:%d retry:%d, want sent:1 retry:0", store.markSentCalls, store.retryCalls)
	}
}

func TestTicketEmailWorkerSchedulesRetryOnSenderError(t *testing.T) {
	store := &fakeTicketEmailStore{email: TicketEmail{To: "guest@example.com"}}
	sender := &fakeTicketEmailSender{err: errors.New("provider timeout")}
	worker := newTestTicketEmailWorker(t, store, sender, 5)
	startedAt := time.Now()

	if err := worker.process(context.Background(), TicketEmailJob{ID: 3, AttemptCount: 2}); err != nil {
		t.Fatalf("process() error = %v", err)
	}
	if store.retryCalls != 1 || store.lastTerminal {
		t.Errorf("retry calls/terminal = %d/%v, want 1/false", store.retryCalls, store.lastTerminal)
	}
	if !store.lastRetryAt.After(startedAt) {
		t.Errorf("retry time = %v, want a future time", store.lastRetryAt)
	}
}

func TestTicketEmailWorkerMarksJobFailedAfterMaximumAttempts(t *testing.T) {
	store := &fakeTicketEmailStore{email: TicketEmail{To: "guest@example.com"}}
	sender := &fakeTicketEmailSender{err: errors.New("provider unavailable")}
	worker := newTestTicketEmailWorker(t, store, sender, 3)

	if err := worker.process(context.Background(), TicketEmailJob{ID: 3, AttemptCount: 3}); err != nil {
		t.Fatalf("process() error = %v", err)
	}
	if store.retryCalls != 1 || !store.lastTerminal {
		t.Errorf("retry calls/terminal = %d/%v, want 1/true", store.retryCalls, store.lastTerminal)
	}
}

func TestTicketEmailWorkerIgnoresLostClaimOnCompletion(t *testing.T) {
	store := &fakeTicketEmailStore{email: TicketEmail{To: "guest@example.com"}, markSentErr: ErrEmailClaimLost}
	worker := newTestTicketEmailWorker(t, store, &fakeTicketEmailSender{}, 5)

	if err := worker.process(context.Background(), TicketEmailJob{ID: 3, AttemptCount: 1}); err != nil {
		t.Fatalf("process() error = %v, want nil for stale claim", err)
	}
}

func newTestTicketEmailWorker(t *testing.T, store *fakeTicketEmailStore, sender *fakeTicketEmailSender, maxAttempts int) *TicketEmailWorker {
	t.Helper()
	worker, err := NewTicketEmailWorker(store, sender, TicketEmailWorkerConfig{
		PollingInterval: time.Second,
		LeaseDuration:   20 * time.Second,
		SendTimeout:     10 * time.Second,
		MaxAttempts:     maxAttempts,
	})
	if err != nil {
		t.Fatalf("NewTicketEmailWorker() error = %v", err)
	}
	return worker
}

type fakeTicketEmailStore struct {
	email         TicketEmail
	markSentErr   error
	markSentCalls int
	retryCalls    int
	lastRetryAt   time.Time
	lastTerminal  bool
}

func (s *fakeTicketEmailStore) ClaimNextTicketEmail(context.Context, time.Duration) (TicketEmailJob, bool, error) {
	return TicketEmailJob{}, false, nil
}

func (s *fakeTicketEmailStore) LoadTicketEmail(context.Context, TicketEmailJob) (TicketEmail, error) {
	return s.email, nil
}

func (s *fakeTicketEmailStore) MarkTicketEmailSent(context.Context, TicketEmailJob) error {
	s.markSentCalls++
	return s.markSentErr
}

func (s *fakeTicketEmailStore) RetryTicketEmail(_ context.Context, _ TicketEmailJob, retryAt time.Time, terminal bool) error {
	s.retryCalls++
	s.lastRetryAt = retryAt
	s.lastTerminal = terminal
	return nil
}

type fakeTicketEmailSender struct {
	err       error
	calls     int
	lastEmail TicketEmail
}

func (s *fakeTicketEmailSender) SendTicketEmail(_ context.Context, email TicketEmail) error {
	s.calls++
	s.lastEmail = email
	return s.err
}
