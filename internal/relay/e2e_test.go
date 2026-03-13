package relay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/spool"
	_ "modernc.org/sqlite"
)

const qaSQLiteDriverName = "sqlite"

type scriptedProvider struct {
	mu          sync.Mutex
	submitSteps []submitStep
	pollSteps   []pollStep
	submitCalls int
	pollCalls   int
}

type submitStep struct {
	fn func(context.Context, email.Message, string) (email.SubmissionResult, error)
}

type pollStep struct {
	fn func(context.Context, string) (email.SubmissionStatus, error)
}

func (p *scriptedProvider) Submit(ctx context.Context, msg email.Message, operationID string) (email.SubmissionResult, error) {
	p.mu.Lock()
	p.submitCalls++
	if len(p.submitSteps) == 0 {
		p.mu.Unlock()
		return email.SubmissionResult{}, fmt.Errorf("unexpected Submit call")
	}
	step := p.submitSteps[0]
	p.submitSteps = p.submitSteps[1:]
	p.mu.Unlock()
	return step.fn(ctx, msg, operationID)
}

func (p *scriptedProvider) Poll(ctx context.Context, operationID string) (email.SubmissionStatus, error) {
	p.mu.Lock()
	p.pollCalls++
	if len(p.pollSteps) == 0 {
		p.mu.Unlock()
		return email.SubmissionStatus{}, fmt.Errorf("unexpected Poll call")
	}
	step := p.pollSteps[0]
	p.pollSteps = p.pollSteps[1:]
	p.mu.Unlock()
	return step.fn(ctx, operationID)
}

func (p *scriptedProvider) submitCallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.submitCalls
}

func (p *scriptedProvider) pollCallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pollCalls
}

type qaDeliveryError struct {
	provider   string
	message    string
	temporary  bool
	statusCode int
}

func (e qaDeliveryError) Error() string        { return e.message }
func (e qaDeliveryError) ProviderName() string { return e.provider }
func (e qaDeliveryError) Temporary() bool      { return e.temporary }
func (e qaDeliveryError) HTTPStatusCode() int  { return e.statusCode }

type persistedRecord struct {
	ID                string
	State             string
	Attempt           int
	OperationID       string
	OperationLocation string
	ProviderMessageID string
	FirstSubmittedAt  time.Time
	LastErrorProvider string
	LastErrorMessage  string
}

func TestE2ERestartRecoveryAfterQueuedButUnsentMessage(t *testing.T) {
	root := t.TempDir()
	store := mustNewQAStore(t, root)
	handler := mustNewHandler(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, store, 1)

	msg := email.Message{
		EnvelopeFrom: "from@example.com",
		HeaderFrom:   "header@example.com",
		To:           []string{"to@example.com"},
		TextBody:     "queued restart",
	}
	if err := handler.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	store = mustNewQAStore(t, root)
	provider := &scriptedProvider{
		submitSteps: []submitStep{{fn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
			return email.SubmissionResult{State: email.SubmissionStateSucceeded, ProviderMessageID: "msg-queued"}, nil
		}}},
	}
	worker := mustNewQAWorker(t, store, "acs", provider)

	recovery, err := worker.Recover(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if len(recovery.Requeued) != 0 || len(recovery.Submitted) != 0 || len(recovery.DeadLettered) != 0 || len(recovery.OrphanedPayloads) != 0 {
		t.Fatalf("unexpected recovery result: %#v", recovery)
	}

	cancel, errCh := startQAWorker(t, worker)
	defer stopQAWorker(t, cancel, errCh)

	rec := waitForSinglePersistedState(t, root, string(spool.StateSucceeded))
	if rec.ProviderMessageID != "msg-queued" {
		t.Fatalf("unexpected provider message id: %q", rec.ProviderMessageID)
	}
	if provider.submitCallCount() != 1 {
		t.Fatalf("unexpected submit call count: %d", provider.submitCallCount())
	}
	if provider.pollCallCount() != 0 {
		t.Fatalf("unexpected poll call count: %d", provider.pollCallCount())
	}
}

func TestE2ERestartRecoveryAfterSubmittedNonTerminalOperation(t *testing.T) {
	root := t.TempDir()
	store := mustNewQAStore(t, root)

	msg := email.Message{
		EnvelopeFrom: "from@example.com",
		HeaderFrom:   "header@example.com",
		To:           []string{"to@example.com"},
		TextBody:     "submitted restart",
	}
	rec, err := store.Enqueue(context.Background(), msg)
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	working, ok, err := store.ClaimReady(context.Background(), time.Now().UTC().Add(time.Second))
	if err != nil || !ok {
		t.Fatalf("ClaimReady() = (%#v, %t, %v)", working, ok, err)
	}
	_, err = store.MarkSubmitted(context.Background(), working, email.SubmissionResult{
		State:             email.SubmissionStateRunning,
		OperationID:       rec.ID,
		OperationLocation: "loc-1",
		ProviderMessageID: "msg-submitted",
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("MarkSubmitted() error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	store = mustNewQAStore(t, root)
	provider := &scriptedProvider{
		pollSteps: []pollStep{{fn: func(_ context.Context, operationID string) (email.SubmissionStatus, error) {
			if operationID != rec.ID {
				t.Fatalf("unexpected poll operation id: %q", operationID)
			}
			return email.SubmissionStatus{State: email.SubmissionStateSucceeded, ProviderMessageID: "msg-final"}, nil
		}}},
	}
	worker := mustNewQAWorker(t, store, "acs", provider)

	recovery, err := worker.Recover(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if len(recovery.Submitted) != 1 {
		t.Fatalf("expected one submitted recovery record, got %#v", recovery)
	}

	cancel, errCh := startQAWorker(t, worker)
	defer stopQAWorker(t, cancel, errCh)

	persisted := waitForRecordStateByID(t, root, rec.ID, string(spool.StateSucceeded))
	if persisted.ProviderMessageID != "msg-final" {
		t.Fatalf("unexpected provider message id: %q", persisted.ProviderMessageID)
	}
	if provider.submitCallCount() != 0 {
		t.Fatalf("unexpected submit call count: %d", provider.submitCallCount())
	}
	if provider.pollCallCount() != 1 {
		t.Fatalf("unexpected poll call count: %d", provider.pollCallCount())
	}
}

func TestE2EMissingPayloadRecoveryIntoDeadLetter(t *testing.T) {
	root := t.TempDir()
	store := mustNewQAStore(t, root)

	rec, err := store.Enqueue(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
		TextBody:     "missing payload",
	})
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if _, ok, err := store.ClaimReady(context.Background(), time.Now().UTC().Add(time.Second)); err != nil || !ok {
		t.Fatalf("ClaimReady() = (%t, %v)", ok, err)
	}
	if err := os.RemoveAll(filepath.Join(root, "payloads", rec.ID)); err != nil {
		t.Fatalf("RemoveAll() error: %v", err)
	}

	worker := mustNewQAWorker(t, store, "acs", &scriptedProvider{})
	recovery, err := worker.Recover(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if len(recovery.DeadLettered) != 1 {
		t.Fatalf("expected one dead-lettered record, got %#v", recovery)
	}

	persisted := waitForRecordStateByID(t, root, rec.ID, string(spool.StateDeadLetter))
	if persisted.LastErrorProvider != "spool" {
		t.Fatalf("unexpected last error provider: %q", persisted.LastErrorProvider)
	}
}

func TestE2EOrphanPayloadQuarantine(t *testing.T) {
	root := t.TempDir()
	store := mustNewQAStore(t, root)
	defer closeQAStore(t, store)

	orphanID := qaRecordID(0x991)
	orphanDir := filepath.Join(root, "payloads", orphanID)
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "message.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	worker := mustNewQAWorker(t, store, "acs", &scriptedProvider{})
	recovery, err := worker.Recover(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if len(recovery.OrphanedPayloads) != 1 {
		t.Fatalf("expected one orphaned payload, got %#v", recovery.OrphanedPayloads)
	}
	if _, err := os.Stat(recovery.OrphanedPayloads[0]); err != nil {
		t.Fatalf("expected quarantined payload path to exist: %v", err)
	}
	if _, err := os.Stat(orphanDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected original orphan payload dir removed, stat err=%v", err)
	}
}

func TestE2ETemporaryACSFailureWithRetry(t *testing.T) {
	qaRetrySuccessScenario(t, "acs")
}

func TestE2EPermanentACSFailureToDeadLetter(t *testing.T) {
	qaPermanentFailureScenario(t, "acs")
}

func TestE2ETemporarySESFailureWithRetry(t *testing.T) {
	qaRetrySuccessScenario(t, "ses")
}

func TestE2EPermanentSESFailureToDeadLetter(t *testing.T) {
	qaPermanentFailureScenario(t, "ses")
}

func TestE2ESenderRewriteMode(t *testing.T) {
	root := t.TempDir()
	store := mustNewQAStore(t, root)
	defer closeQAStore(t, store)

	handler := mustNewHandler(t, email.SenderPolicyOptions{
		Mode:                  email.SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"allowed.example.com"},
	}, store, 1)

	msg := email.Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "header@example.com",
		ReplyTo:      []string{"reply@blocked.example.com"},
		To:           []string{"to@example.com"},
		TextBody:     "rewrite",
	}
	if err := handler.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	rec, ok, err := store.ClaimReady(context.Background(), time.Now().UTC().Add(time.Second))
	if err != nil || !ok {
		t.Fatalf("ClaimReady() = (%#v, %t, %v)", rec, ok, err)
	}
	if len(rec.Message.ReplyTo) != 0 {
		t.Fatalf("expected rewrite mode to clear disallowed reply-to, got %#v", rec.Message.ReplyTo)
	}
	if rec.Message.EnvelopeFrom != msg.EnvelopeFrom || rec.Message.HeaderFrom != msg.HeaderFrom {
		t.Fatalf("unexpected preserved sender fields: %#v", rec.Message)
	}
}

func TestE2ESenderStrictRejection(t *testing.T) {
	root := t.TempDir()
	store := mustNewQAStore(t, root)
	defer closeQAStore(t, store)

	handler := mustNewHandler(t, email.SenderPolicyOptions{
		Mode:                  email.SenderPolicyStrict,
		AllowedDomainPatterns: []string{"allowed.example.com"},
	}, store, 1)

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "sender@blocked.example.com",
		To:           []string{"to@example.com"},
	})
	if _, ok := email.AsSenderPolicyError(err); !ok {
		t.Fatalf("expected SenderPolicyError, got %T: %v", err, err)
	}

	counts, err := store.StateCounts(context.Background())
	if err != nil {
		t.Fatalf("StateCounts() error: %v", err)
	}
	if totalStateCount(counts) != 0 {
		t.Fatalf("expected no durable spool records, got %#v", counts)
	}
}

func qaRetrySuccessScenario(t *testing.T, providerName string) {
	t.Helper()

	root := t.TempDir()
	store := mustNewQAStore(t, root)
	handler := mustNewHandler(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, store, 1)
	provider := &scriptedProvider{
		submitSteps: []submitStep{
			{fn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
				return email.SubmissionResult{}, qaDeliveryError{provider: providerName, message: "temporary", temporary: true, statusCode: 503}
			}},
			{fn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
				return email.SubmissionResult{State: email.SubmissionStateSucceeded, ProviderMessageID: providerName + "-msg"}, nil
			}},
		},
	}
	worker := mustNewQAWorker(t, store, providerName, provider)

	if err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
		TextBody:     "retry",
	}); err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	cancel, errCh := startQAWorker(t, worker)
	defer stopQAWorker(t, cancel, errCh)

	rec := waitForSinglePersistedState(t, root, string(spool.StateSucceeded))
	if rec.Attempt != 2 {
		t.Fatalf("expected attempt count 2 after retry, got %d", rec.Attempt)
	}
	if rec.ProviderMessageID != providerName+"-msg" {
		t.Fatalf("unexpected provider message id: %q", rec.ProviderMessageID)
	}
	if provider.submitCallCount() != 2 {
		t.Fatalf("unexpected submit call count: %d", provider.submitCallCount())
	}
}

func qaPermanentFailureScenario(t *testing.T, providerName string) {
	t.Helper()

	root := t.TempDir()
	store := mustNewQAStore(t, root)
	handler := mustNewHandler(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, store, 1)
	provider := &scriptedProvider{
		submitSteps: []submitStep{{fn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
			return email.SubmissionResult{}, qaDeliveryError{provider: providerName, message: "permanent", temporary: false, statusCode: 400}
		}}},
	}
	worker := mustNewQAWorker(t, store, providerName, provider)

	if err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
		TextBody:     "dead-letter",
	}); err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	cancel, errCh := startQAWorker(t, worker)
	defer stopQAWorker(t, cancel, errCh)

	rec := waitForSinglePersistedState(t, root, string(spool.StateDeadLetter))
	if rec.Attempt != 1 {
		t.Fatalf("expected attempt count 1 after permanent failure, got %d", rec.Attempt)
	}
	if rec.LastErrorProvider != providerName {
		t.Fatalf("unexpected last error provider: %q", rec.LastErrorProvider)
	}
	if provider.submitCallCount() != 1 {
		t.Fatalf("unexpected submit call count: %d", provider.submitCallCount())
	}
}

func mustNewQAStore(t *testing.T, root string) *spool.SpoolStore {
	t.Helper()
	store, err := spool.NewSpoolStore(root)
	if err != nil {
		t.Fatalf("NewSpoolStore() error: %v", err)
	}
	return store
}

func closeQAStore(t *testing.T, store *spool.SpoolStore) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func mustNewQAWorker(t *testing.T, store *spool.SpoolStore, providerName string, provider email.Provider) *spool.Worker {
	t.Helper()
	worker, err := spool.NewWorker(testLogger(), store, provider, spool.WorkerConfig{
		SubmitTimeout:    time.Second,
		FinalizeTimeout:  time.Second,
		PollInterval:     10 * time.Millisecond,
		RetryAttempts:    3,
		RetryBaseDelay:   10 * time.Millisecond,
		SubmittedTimeout: spool.DefaultSubmittedTimeout,
		ProviderName:     providerName,
	})
	if err != nil {
		t.Fatalf("NewWorker() error: %v", err)
	}
	return worker
}

func startQAWorker(t *testing.T, worker *spool.Worker) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- worker.Start(ctx)
	}()
	return cancel, errCh
}

func stopQAWorker(t *testing.T, cancel context.CancelFunc, errCh <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("worker shutdown error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker shutdown")
	}
}

func waitForSinglePersistedState(t *testing.T, root string, state string) persistedRecord {
	t.Helper()
	var got persistedRecord
	waitFor(t, 3*time.Second, func() bool {
		records := readPersistedRecordsByState(t, root, state)
		if len(records) != 1 {
			return false
		}
		got = records[0]
		return true
	})
	return got
}

func waitForRecordStateByID(t *testing.T, root string, id string, state string) persistedRecord {
	t.Helper()
	var got persistedRecord
	waitFor(t, 3*time.Second, func() bool {
		rec, ok := readPersistedRecordByID(t, root, id)
		if !ok || rec.State != state {
			return false
		}
		got = rec
		return true
	})
	return got
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func readPersistedRecordsByState(t *testing.T, root string, state string) []persistedRecord {
	t.Helper()
	db := openQASpoolDB(t, root)
	defer db.Close()

	rows, err := db.Query(`
		SELECT id, state, attempt, COALESCE(operation_id, ''), COALESCE(operation_location, ''),
		       COALESCE(provider_message_id, ''), first_submitted_at_ms,
		       COALESCE(last_error_provider, ''), COALESCE(last_error_message, '')
		FROM records
		WHERE state = ?
		ORDER BY created_at_ms ASC, id ASC`, state)
	if err != nil {
		t.Fatalf("Query(state=%q) error: %v", state, err)
	}
	defer rows.Close()

	var out []persistedRecord
	for rows.Next() {
		var (
			rec            persistedRecord
			firstSubmitted sql.NullInt64
		)
		if err := rows.Scan(
			&rec.ID,
			&rec.State,
			&rec.Attempt,
			&rec.OperationID,
			&rec.OperationLocation,
			&rec.ProviderMessageID,
			&firstSubmitted,
			&rec.LastErrorProvider,
			&rec.LastErrorMessage,
		); err != nil {
			t.Fatalf("Scan(state=%q) error: %v", state, err)
		}
		if firstSubmitted.Valid {
			rec.FirstSubmittedAt = time.UnixMilli(firstSubmitted.Int64).UTC()
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Rows(state=%q) error: %v", state, err)
	}
	return out
}

func readPersistedRecordByID(t *testing.T, root string, id string) (persistedRecord, bool) {
	t.Helper()
	db := openQASpoolDB(t, root)
	defer db.Close()

	var (
		rec            persistedRecord
		firstSubmitted sql.NullInt64
	)
	err := db.QueryRow(`
		SELECT id, state, attempt, COALESCE(operation_id, ''), COALESCE(operation_location, ''),
		       COALESCE(provider_message_id, ''), first_submitted_at_ms,
		       COALESCE(last_error_provider, ''), COALESCE(last_error_message, '')
		FROM records
		WHERE id = ?`, id).Scan(
		&rec.ID,
		&rec.State,
		&rec.Attempt,
		&rec.OperationID,
		&rec.OperationLocation,
		&rec.ProviderMessageID,
		&firstSubmitted,
		&rec.LastErrorProvider,
		&rec.LastErrorMessage,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return persistedRecord{}, false
	}
	if err != nil {
		t.Fatalf("QueryRow(id=%q) error: %v", id, err)
	}
	if firstSubmitted.Valid {
		rec.FirstSubmittedAt = time.UnixMilli(firstSubmitted.Int64).UTC()
	}
	return rec, true
}

func openQASpoolDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open(qaSQLiteDriverName, filepath.Join(root, "spool.db"))
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	return db
}

func qaRecordID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
}

func totalStateCount(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}
