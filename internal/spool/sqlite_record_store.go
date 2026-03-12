package spool

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// sqliteRecordStore owns SQLite-backed spool record metadata only.
type sqliteRecordStore struct {
	root string
	db   *sql.DB
	now  func() time.Time
}

type recordMetadata struct {
	ID                string
	State             State
	Attempt           int
	NextAttemptAt     time.Time
	OperationID       string
	OperationLocation string
	LastError         *LastError
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// recordRecoverySnapshot captures metadata-only recovery state before payload verification.
type recordRecoverySnapshot struct {
	requeued  []recordMetadata
	submitted []recordMetadata
	validIDs  map[string]struct{}
}

func newSQLiteRecordStore(root string, now func() time.Time) (*sqliteRecordStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("spool root cannot be empty")
	}
	rootFD, cleanRoot, err := ensureDirectoryPathNoFollow(root)
	if err != nil {
		return nil, fmt.Errorf("ensure spool root %s: %w", root, err)
	}
	defer unixClose(rootFD)

	db, err := sql.Open(sqliteDriverName, filepath.Join(cleanRoot, spoolDBFileName))
	if err != nil {
		return nil, wrapStoreError("open sqlite database", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &sqliteRecordStore{
		root: cleanRoot,
		db:   db,
		now:  now,
	}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *sqliteRecordStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *sqliteRecordStore) insertRecord(ctx context.Context, rec Record) error {
	if err := validateCanonicalRecordID(rec.ID); err != nil {
		return err
	}

	conn, err := s.beginImmediate(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer s.finishImmediate(ctx, conn, &committed)

	if _, err := conn.ExecContext(ctx, `
		INSERT INTO records (
			id, state, attempt, next_attempt_at_ms, operation_id, operation_location,
			last_error_message, last_error_provider, last_error_temporary, last_error_timestamp_ms,
			created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID,
		string(rec.State),
		rec.Attempt,
		timeToMillis(rec.NextAttemptAt),
		nullIfEmpty(rec.OperationID),
		nullIfEmpty(rec.OperationLocation),
		lastErrorMessage(rec.LastError),
		lastErrorProvider(rec.LastError),
		lastErrorTemporary(rec.LastError),
		lastErrorTimestamp(rec.LastError),
		timeToMillis(rec.CreatedAt),
		timeToMillis(rec.UpdatedAt),
	); err != nil {
		return wrapStoreError(fmt.Sprintf("insert record %q", rec.ID), err)
	}

	if err := commitImmediate(ctx, conn); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *sqliteRecordStore) claimReadyMetadata(ctx context.Context, now time.Time) (recordMetadata, bool, error) {
	conn, err := s.beginImmediate(ctx)
	if err != nil {
		return recordMetadata{}, false, err
	}
	committed := false
	defer s.finishImmediate(ctx, conn, &committed)

	meta, ok, err := queryOneMetadata(ctx, conn, `
		SELECT
			id, state, attempt, next_attempt_at_ms, operation_id, operation_location,
			last_error_message, last_error_provider, last_error_temporary, last_error_timestamp_ms,
			created_at_ms, updated_at_ms
		FROM records
		WHERE state = ? AND next_attempt_at_ms <= ?
		ORDER BY next_attempt_at_ms ASC, created_at_ms ASC, id ASC
		LIMIT 1`,
		string(StateQueued), timeToMillis(now))
	if err != nil || !ok {
		return recordMetadata{}, ok, err
	}

	meta.State = StateWorking
	meta.UpdatedAt = s.now().UTC()
	result, err := conn.ExecContext(ctx, `
		UPDATE records
		SET state = ?, updated_at_ms = ?
		WHERE id = ? AND state = ?`,
		string(meta.State),
		timeToMillis(meta.UpdatedAt),
		meta.ID,
		string(StateQueued),
	)
	if err != nil {
		return recordMetadata{}, false, wrapStoreError(fmt.Sprintf("claim record %q", meta.ID), err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return recordMetadata{}, false, wrapStoreError(fmt.Sprintf("read claim result for %q", meta.ID), err)
	}
	if rowsAffected != 1 {
		return recordMetadata{}, false, fmt.Errorf("spool record %q was not claimable", meta.ID)
	}

	if err := commitImmediate(ctx, conn); err != nil {
		return recordMetadata{}, false, err
	}
	committed = true
	return meta, true, nil
}

func (s *sqliteRecordStore) markSubmitted(ctx context.Context, rec Record, operationID, operationLocation string, nextAttemptAt time.Time) (Record, error) {
	if rec.State != StateWorking {
		return Record{}, fmt.Errorf("mark submitted requires %q state, got %q", StateWorking, rec.State)
	}

	updated := rec
	updated.State = StateSubmitted
	updated.Attempt = rec.Attempt + 1
	updated.OperationID = strings.TrimSpace(operationID)
	updated.OperationLocation = strings.TrimSpace(operationLocation)
	updated.NextAttemptAt = nextAttemptAt.UTC()
	updated.LastError = nil
	updated.UpdatedAt = s.now().UTC()

	if err := s.updateRecord(ctx, rec.State, updated); err != nil {
		return Record{}, err
	}
	return updated, nil
}

func (s *sqliteRecordStore) markRetry(ctx context.Context, rec Record, nextAttemptAt time.Time, lastErr *LastError) (Record, error) {
	updated := rec
	updated.NextAttemptAt = nextAttemptAt.UTC()
	updated.LastError = normalizeLastError(lastErr, s.now().UTC())
	updated.UpdatedAt = s.now().UTC()

	switch rec.State {
	case StateWorking:
		updated.State = StateQueued
		updated.Attempt = rec.Attempt + 1
		updated.OperationID = ""
		updated.OperationLocation = ""
	case StateSubmitted:
		updated.State = StateSubmitted
	default:
		return Record{}, fmt.Errorf("mark retry requires %q or %q state, got %q", StateWorking, StateSubmitted, rec.State)
	}

	if err := s.updateRecord(ctx, rec.State, updated); err != nil {
		return Record{}, err
	}
	return updated, nil
}

func (s *sqliteRecordStore) markSucceeded(ctx context.Context, rec Record) (Record, error) {
	updated := rec
	updated.State = StateSucceeded
	updated.LastError = nil
	updated.UpdatedAt = s.now().UTC()

	switch rec.State {
	case StateWorking:
		updated.Attempt = rec.Attempt + 1
	case StateSubmitted:
		updated.Attempt = rec.Attempt
	default:
		return Record{}, fmt.Errorf("mark succeeded requires %q or %q state, got %q", StateWorking, StateSubmitted, rec.State)
	}

	if err := s.updateRecord(ctx, rec.State, updated); err != nil {
		return Record{}, err
	}
	return updated, nil
}

func (s *sqliteRecordStore) markDeadLetter(ctx context.Context, rec Record, lastErr *LastError) (Record, error) {
	updated := rec
	updated.State = StateDeadLetter
	updated.LastError = normalizeLastError(lastErr, s.now().UTC())
	updated.UpdatedAt = s.now().UTC()

	switch rec.State {
	case StateWorking:
		updated.Attempt = rec.Attempt + 1
		updated.OperationID = ""
		updated.OperationLocation = ""
	case StateSubmitted:
		updated.Attempt = rec.Attempt
	default:
		return Record{}, fmt.Errorf("mark dead-letter requires %q or %q state, got %q", StateWorking, StateSubmitted, rec.State)
	}

	if err := s.updateRecord(ctx, rec.State, updated); err != nil {
		return Record{}, err
	}
	return updated, nil
}

func (s *sqliteRecordStore) requeueClaimedRecord(ctx context.Context, meta recordMetadata) error {
	updated := meta.toRecord()
	updated.State = StateQueued
	updated.UpdatedAt = s.now().UTC()
	if err := s.updateRecord(ctx, meta.State, updated); err != nil {
		return wrapStoreError(fmt.Sprintf("requeue claimed record %q", meta.ID), err)
	}
	return nil
}

func (s *sqliteRecordStore) recoverMetadata(ctx context.Context, now time.Time) (recordRecoverySnapshot, error) {
	conn, err := s.beginImmediate(ctx)
	if err != nil {
		return recordRecoverySnapshot{}, err
	}
	committed := false
	defer s.finishImmediate(ctx, conn, &committed)

	working, err := queryAllMetadata(ctx, conn, `
		SELECT
			id, state, attempt, next_attempt_at_ms, operation_id, operation_location,
			last_error_message, last_error_provider, last_error_temporary, last_error_timestamp_ms,
			created_at_ms, updated_at_ms
		FROM records
		WHERE state = ?
		ORDER BY created_at_ms ASC, id ASC`, string(StateWorking))
	if err != nil {
		return recordRecoverySnapshot{}, err
	}
	submitted, err := queryAllMetadata(ctx, conn, `
		SELECT
			id, state, attempt, next_attempt_at_ms, operation_id, operation_location,
			last_error_message, last_error_provider, last_error_temporary, last_error_timestamp_ms,
			created_at_ms, updated_at_ms
		FROM records
		WHERE state = ?
		ORDER BY created_at_ms ASC, id ASC`, string(StateSubmitted))
	if err != nil {
		return recordRecoverySnapshot{}, err
	}
	validIDs, err := queryAllIDs(ctx, conn)
	if err != nil {
		return recordRecoverySnapshot{}, err
	}

	requeued := make([]recordMetadata, 0, len(working))
	for _, meta := range working {
		requeuedMeta := meta
		requeuedMeta.State = StateQueued
		requeuedMeta.NextAttemptAt = now
		requeuedMeta.OperationID = ""
		requeuedMeta.OperationLocation = ""
		requeuedMeta.UpdatedAt = s.now().UTC()

		if _, err := conn.ExecContext(ctx, `
			UPDATE records
			SET state = ?, next_attempt_at_ms = ?, operation_id = NULL, operation_location = NULL, updated_at_ms = ?
			WHERE id = ? AND state = ?`,
			string(requeuedMeta.State),
			timeToMillis(requeuedMeta.NextAttemptAt),
			timeToMillis(requeuedMeta.UpdatedAt),
			requeuedMeta.ID,
			string(StateWorking),
		); err != nil {
			return recordRecoverySnapshot{}, wrapStoreError(fmt.Sprintf("requeue stale working record %q", requeuedMeta.ID), err)
		}

		requeued = append(requeued, requeuedMeta)
	}

	if err := commitImmediate(ctx, conn); err != nil {
		return recordRecoverySnapshot{}, err
	}
	committed = true
	return recordRecoverySnapshot{
		requeued:  requeued,
		submitted: submitted,
		validIDs:  validIDs,
	}, nil
}

func (s *sqliteRecordStore) deadLetterCorruptRecord(ctx context.Context, meta recordMetadata, cause error) (Record, error) {
	now := s.now().UTC()
	updated := meta
	updated.State = StateDeadLetter
	updated.LastError = &LastError{
		Message:   cause.Error(),
		Provider:  spoolProviderName,
		Temporary: false,
		Timestamp: now,
	}
	updated.UpdatedAt = now
	if meta.State != StateSubmitted {
		updated.OperationID = ""
		updated.OperationLocation = ""
	}

	rec := updated.toRecord()
	if err := s.updateRecord(ctx, meta.State, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func (s *sqliteRecordStore) init(ctx context.Context) error {
	if err := s.applyPragmas(ctx); err != nil {
		return err
	}

	version, err := s.userVersion(ctx)
	if err != nil {
		return err
	}

	switch version {
	case 0:
		exists, err := tableExists(ctx, s.db, recordsTableName)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("unsupported local spool schema in %s: delete the spool directory and restart", s.root)
		}
		if err := s.bootstrapSchema(ctx); err != nil {
			return err
		}
	case spoolSchemaVersion:
		if err := s.validateCurrentSchema(ctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported spool schema version %d", version)
	}
	return nil
}

func (s *sqliteRecordStore) updateRecord(ctx context.Context, currentState State, updated Record) error {
	if err := validateCanonicalRecordID(updated.ID); err != nil {
		return err
	}
	if !isValidState(currentState) || !isValidState(updated.State) {
		return fmt.Errorf("invalid state transition %q -> %q", currentState, updated.State)
	}

	conn, err := s.beginImmediate(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer s.finishImmediate(ctx, conn, &committed)

	result, err := conn.ExecContext(ctx, `
		UPDATE records
		SET
			state = ?,
			attempt = ?,
			next_attempt_at_ms = ?,
			operation_id = ?,
			operation_location = ?,
			last_error_message = ?,
			last_error_provider = ?,
			last_error_temporary = ?,
			last_error_timestamp_ms = ?,
			created_at_ms = ?,
			updated_at_ms = ?
		WHERE id = ? AND state = ?`,
		string(updated.State),
		updated.Attempt,
		timeToMillis(updated.NextAttemptAt),
		nullIfEmpty(updated.OperationID),
		nullIfEmpty(updated.OperationLocation),
		lastErrorMessage(updated.LastError),
		lastErrorProvider(updated.LastError),
		lastErrorTemporary(updated.LastError),
		lastErrorTimestamp(updated.LastError),
		timeToMillis(updated.CreatedAt),
		timeToMillis(updated.UpdatedAt),
		updated.ID,
		string(currentState),
	)
	if err != nil {
		return wrapStoreError(fmt.Sprintf("update record %q", updated.ID), err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return wrapStoreError(fmt.Sprintf("read update result for %q", updated.ID), err)
	}
	if rows != 1 {
		return fmt.Errorf("spool record %q not found in state %q", updated.ID, currentState)
	}

	if err := commitImmediate(ctx, conn); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *sqliteRecordStore) applyPragmas(ctx context.Context) error {
	pragmas := []string{
		fmt.Sprintf("PRAGMA journal_mode=%s;", sqliteJournalModeWAL),
		"PRAGMA synchronous=FULL;",
		"PRAGMA foreign_keys=ON;",
		fmt.Sprintf("PRAGMA busy_timeout=%d;", sqliteBusyTimeoutMS),
	}
	for _, pragma := range pragmas {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return wrapStoreError(fmt.Sprintf("apply sqlite pragma %q", pragma), err)
		}
	}
	return nil
}

func (s *sqliteRecordStore) userVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&version); err != nil {
		return 0, wrapStoreError("query sqlite user_version", err)
	}
	return version, nil
}

func (s *sqliteRecordStore) bootstrapSchema(ctx context.Context) error {
	conn, err := s.beginImmediate(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer s.finishImmediate(ctx, conn, &committed)

	if _, err := conn.ExecContext(ctx, recordsTableSchema); err != nil {
		return wrapStoreError("create current spool schema", err)
	}

	for _, stmt := range []string{recordsReadyIndexSchema, recordsOperationIndexSchema} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return wrapStoreError("create spool index", err)
		}
	}

	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d;", spoolSchemaVersion)); err != nil {
		return wrapStoreError("set sqlite user_version", err)
	}

	if err := commitImmediate(ctx, conn); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *sqliteRecordStore) validateCurrentSchema(ctx context.Context) error {
	exists, err := tableExists(ctx, s.db, recordsTableName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("spool schema version %d is missing %q table in %s; delete the spool directory and restart", spoolSchemaVersion, recordsTableName, s.root)
	}

	columns, err := loadTableColumns(ctx, s.db, recordsTableName)
	if err != nil {
		return err
	}
	expectedColumns := []string{
		"id",
		"state",
		"attempt",
		"next_attempt_at_ms",
		"operation_id",
		"operation_location",
		"last_error_message",
		"last_error_provider",
		"last_error_temporary",
		"last_error_timestamp_ms",
		"created_at_ms",
		"updated_at_ms",
	}
	if len(columns) != len(expectedColumns) {
		return fmt.Errorf("unsupported local spool schema in %s: expected %d columns in %q, found %d; delete the spool directory and restart", s.root, len(expectedColumns), recordsTableName, len(columns))
	}
	for i, expected := range expectedColumns {
		if columns[i] != expected {
			return fmt.Errorf("unsupported local spool schema in %s: unexpected column %d in %q (got %q, want %q); delete the spool directory and restart", s.root, i, recordsTableName, columns[i], expected)
		}
	}

	sqlText, err := tableSQL(ctx, s.db, recordsTableName)
	if err != nil {
		return err
	}
	normalizedSQL := normalizeSQL(sqlText)
	requiredSnippets := []string{
		normalizeSQL("create table records"),
		normalizeSQL("id text not null primary key"),
		normalizeSQL("check(length(id) > 0)"),
		normalizeSQL("check(id = trim(id))"),
		normalizeSQL("state text not null check(state in ('queued', 'working', 'submitted', 'succeeded', 'dead-letter'))"),
		normalizeSQL("attempt integer not null check(attempt >= 0)"),
		normalizeSQL("next_attempt_at_ms integer not null check(next_attempt_at_ms > 0)"),
		normalizeSQL("created_at_ms integer not null check(created_at_ms > 0)"),
		normalizeSQL("updated_at_ms integer not null check(updated_at_ms > 0 and updated_at_ms >= created_at_ms)"),
		normalizeSQL("last_error_temporary integer check(last_error_temporary is null or last_error_temporary in (0, 1))"),
		normalizeSQL("last_error_timestamp_ms integer check(last_error_timestamp_ms is null or last_error_timestamp_ms > 0)"),
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(normalizedSQL, snippet) {
			return fmt.Errorf("unsupported local spool schema in %s: %q does not match the expected v1 definition; delete the spool directory and restart", s.root, recordsTableName)
		}
	}

	requiredIndexes := map[string]string{
		recordsReadyIndexName: normalizeSQL("create index idx_records_ready on records(state, next_attempt_at_ms, created_at_ms, id)"),
		recordsOpIndexName:    normalizeSQL("create index idx_records_operation_id on records(operation_id)"),
	}
	for indexName, expectedSQL := range requiredIndexes {
		sqlText, err := indexSQL(ctx, s.db, indexName)
		if err != nil {
			return fmt.Errorf("unsupported local spool schema in %s: missing or invalid %q index; delete the spool directory and restart", s.root, indexName)
		}
		normalizedIndexSQL := normalizeSQL(sqlText)
		if !strings.Contains(normalizedIndexSQL, expectedSQL) {
			return fmt.Errorf("unsupported local spool schema in %s: %q does not match the expected v1 definition; delete the spool directory and restart", s.root, indexName)
		}
	}
	return nil
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	if err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&count); err != nil {
		return false, wrapStoreError(fmt.Sprintf("query sqlite table %q", name), err)
	}
	return count == 1, nil
}

func loadTableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s);", table))
	if err != nil {
		return nil, wrapStoreError(fmt.Sprintf("query sqlite table info for %q", table), err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return nil, wrapStoreError(fmt.Sprintf("scan sqlite table info for %q", table), err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapStoreError(fmt.Sprintf("iterate sqlite table info for %q", table), err)
	}
	return columns, nil
}

func tableSQL(ctx context.Context, db *sql.DB, table string) (string, error) {
	var sqlText sql.NullString
	if err := db.QueryRowContext(
		ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&sqlText); err != nil {
		return "", wrapStoreError(fmt.Sprintf("query sqlite sql for table %q", table), err)
	}
	if !sqlText.Valid || strings.TrimSpace(sqlText.String) == "" {
		return "", fmt.Errorf("sqlite table %q has no schema SQL", table)
	}
	return sqlText.String, nil
}

func indexSQL(ctx context.Context, db *sql.DB, index string) (string, error) {
	var sqlText sql.NullString
	if err := db.QueryRowContext(
		ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`,
		index,
	).Scan(&sqlText); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("sqlite index %q has no schema SQL", index)
		}
		return "", wrapStoreError(fmt.Sprintf("query sqlite sql for index %q", index), err)
	}
	if !sqlText.Valid || strings.TrimSpace(sqlText.String) == "" {
		return "", fmt.Errorf("sqlite index %q has no schema SQL", index)
	}
	return sqlText.String, nil
}

func normalizeSQL(sqlText string) string {
	return strings.ToLower(strings.Join(strings.Fields(sqlText), " "))
}

func (s *sqliteRecordStore) beginImmediate(ctx context.Context) (*sql.Conn, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, wrapStoreError("acquire sqlite connection", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		return nil, wrapStoreError("begin immediate sqlite transaction", err)
	}
	return conn, nil
}

func (s *sqliteRecordStore) finishImmediate(ctx context.Context, conn *sql.Conn, committed *bool) {
	if conn == nil {
		return
	}
	if committed != nil && *committed {
		_ = conn.Close()
		return
	}
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
	_ = conn.Close()
}

func commitImmediate(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		_ = conn.Close()
		return wrapStoreError("commit sqlite transaction", err)
	}
	if err := conn.Close(); err != nil {
		return wrapStoreError("close sqlite connection", err)
	}
	return nil
}

func queryOneMetadata(ctx context.Context, conn *sql.Conn, query string, args ...any) (recordMetadata, bool, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return recordMetadata{}, false, wrapStoreError("query spool metadata", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return recordMetadata{}, false, wrapStoreError("iterate spool metadata", err)
		}
		return recordMetadata{}, false, nil
	}
	meta, err := scanMetadata(rows)
	if err != nil {
		return recordMetadata{}, false, err
	}
	return meta, true, nil
}

func queryAllMetadata(ctx context.Context, conn *sql.Conn, query string, args ...any) ([]recordMetadata, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapStoreError("query spool metadata", err)
	}
	defer rows.Close()

	var out []recordMetadata
	for rows.Next() {
		meta, err := scanMetadata(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapStoreError("iterate spool metadata", err)
	}
	return out, nil
}

func queryAllIDs(ctx context.Context, conn *sql.Conn) (map[string]struct{}, error) {
	rows, err := conn.QueryContext(ctx, `SELECT id FROM records`)
	if err != nil {
		return nil, wrapStoreError("query spool ids", err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, wrapStoreError("scan spool id", err)
		}
		if !id.Valid {
			return nil, wrapStoreError("validate spool ids", fmt.Errorf("spool record row missing id"))
		}
		if err := validateCanonicalRecordID(id.String); err != nil {
			return nil, wrapStoreError("validate spool ids", err)
		}
		out[id.String] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapStoreError("iterate spool ids", err)
	}
	return out, nil
}

func scanMetadata(scanner interface {
	Scan(dest ...any) error
}) (recordMetadata, error) {
	var (
		meta               recordMetadata
		id                 sql.NullString
		state              string
		nextAttemptAtMS    int64
		createdAtMS        int64
		updatedAtMS        int64
		operationID        sql.NullString
		operationLocation  sql.NullString
		lastErrMessage     sql.NullString
		lastErrProvider    sql.NullString
		lastErrTemporary   sql.NullInt64
		lastErrTimestampMS sql.NullInt64
	)
	if err := scanner.Scan(
		&id,
		&state,
		&meta.Attempt,
		&nextAttemptAtMS,
		&operationID,
		&operationLocation,
		&lastErrMessage,
		&lastErrProvider,
		&lastErrTemporary,
		&lastErrTimestampMS,
		&createdAtMS,
		&updatedAtMS,
	); err != nil {
		return recordMetadata{}, wrapStoreError("scan spool metadata", err)
	}

	if !id.Valid {
		return recordMetadata{}, wrapStoreError("validate spool metadata", fmt.Errorf("spool record row missing id"))
	}
	meta.ID = id.String
	meta.State = State(state)
	if err := validateCanonicalRecordID(meta.ID); err != nil {
		return recordMetadata{}, wrapStoreError("validate spool metadata", err)
	}
	if !isValidState(meta.State) {
		return recordMetadata{}, wrapStoreError("validate spool metadata", fmt.Errorf("spool record %q has invalid state %q", meta.ID, meta.State))
	}
	if meta.Attempt < 0 {
		return recordMetadata{}, wrapStoreError("validate spool metadata", fmt.Errorf("spool record %q has negative attempt %d", meta.ID, meta.Attempt))
	}
	if nextAttemptAtMS <= 0 {
		return recordMetadata{}, wrapStoreError("validate spool metadata", fmt.Errorf("spool record %q has invalid next_attempt_at_ms %d", meta.ID, nextAttemptAtMS))
	}
	if createdAtMS <= 0 {
		return recordMetadata{}, wrapStoreError("validate spool metadata", fmt.Errorf("spool record %q has invalid created_at_ms %d", meta.ID, createdAtMS))
	}
	if updatedAtMS <= 0 {
		return recordMetadata{}, wrapStoreError("validate spool metadata", fmt.Errorf("spool record %q has invalid updated_at_ms %d", meta.ID, updatedAtMS))
	}
	meta.NextAttemptAt = millisToTime(nextAttemptAtMS)
	meta.OperationID = strings.TrimSpace(operationID.String)
	meta.OperationLocation = strings.TrimSpace(operationLocation.String)
	meta.CreatedAt = millisToTime(createdAtMS)
	meta.UpdatedAt = millisToTime(updatedAtMS)
	if meta.UpdatedAt.Before(meta.CreatedAt) {
		return recordMetadata{}, wrapStoreError("validate spool metadata", fmt.Errorf("spool record %q has updated_at before created_at", meta.ID))
	}
	if lastErrMessage.Valid || lastErrProvider.Valid || lastErrTemporary.Valid || lastErrTimestampMS.Valid {
		if lastErrTemporary.Valid && lastErrTemporary.Int64 != 0 && lastErrTemporary.Int64 != 1 {
			return recordMetadata{}, wrapStoreError("validate spool metadata", fmt.Errorf("spool record %q has invalid last_error_temporary %d", meta.ID, lastErrTemporary.Int64))
		}
		if !lastErrTimestampMS.Valid || lastErrTimestampMS.Int64 <= 0 {
			return recordMetadata{}, wrapStoreError("validate spool metadata", fmt.Errorf("spool record %q has incomplete last error timestamp state", meta.ID))
		}
		meta.LastError = &LastError{
			Message:   strings.TrimSpace(lastErrMessage.String),
			Provider:  strings.TrimSpace(lastErrProvider.String),
			Temporary: lastErrTemporary.Int64 != 0,
			Timestamp: millisToTime(lastErrTimestampMS.Int64),
		}
	}

	return meta, nil
}

func (m recordMetadata) toRecord() Record {
	return Record{
		ID:                m.ID,
		State:             m.State,
		Attempt:           m.Attempt,
		NextAttemptAt:     m.NextAttemptAt,
		OperationID:       m.OperationID,
		OperationLocation: m.OperationLocation,
		LastError:         normalizeLastError(m.LastError, time.Time{}),
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
	}
}

func timeToMillis(ts time.Time) int64 {
	if ts.IsZero() {
		return 0
	}
	return ts.UTC().UnixMilli()
}

func millisToTime(v int64) time.Time {
	return time.UnixMilli(v).UTC()
}

func nullIfEmpty(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func lastErrorMessage(lastErr *LastError) any {
	if lastErr == nil || strings.TrimSpace(lastErr.Message) == "" {
		return nil
	}
	return strings.TrimSpace(lastErr.Message)
}

func lastErrorProvider(lastErr *LastError) any {
	if lastErr == nil || strings.TrimSpace(lastErr.Provider) == "" {
		return nil
	}
	return strings.TrimSpace(lastErr.Provider)
}

func lastErrorTemporary(lastErr *LastError) any {
	if lastErr == nil {
		return nil
	}
	if lastErr.Temporary {
		return 1
	}
	return 0
}

func lastErrorTimestamp(lastErr *LastError) any {
	if lastErr == nil || lastErr.Timestamp.IsZero() {
		return nil
	}
	return lastErr.Timestamp.UTC().UnixMilli()
}
