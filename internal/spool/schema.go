package spool

const (
	spoolSchemaVersion    = 1
	recordsTableName      = "records"
	recordsReadyIndexName = "idx_records_ready"
	recordsOpIndexName    = "idx_records_operation_id"
)

const recordsTableSchema = `
CREATE TABLE IF NOT EXISTS records (
	id TEXT NOT NULL PRIMARY KEY CHECK(length(id) > 0) CHECK(id = trim(id)),
	state TEXT NOT NULL CHECK(state IN ('queued', 'working', 'submitted', 'succeeded', 'dead-letter')),
	attempt INTEGER NOT NULL CHECK(attempt >= 0),
	next_attempt_at_ms INTEGER NOT NULL CHECK(next_attempt_at_ms > 0),
	operation_id TEXT,
	operation_location TEXT,
	provider_message_id TEXT,
	first_submitted_at_ms INTEGER CHECK(first_submitted_at_ms IS NULL OR first_submitted_at_ms > 0),
	last_error_message TEXT,
	last_error_provider TEXT,
	last_error_temporary INTEGER CHECK(last_error_temporary IS NULL OR last_error_temporary IN (0, 1)),
	last_error_timestamp_ms INTEGER CHECK(last_error_timestamp_ms IS NULL OR last_error_timestamp_ms > 0),
	created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
	updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms > 0 AND updated_at_ms >= created_at_ms),
	CHECK(
		(last_error_message IS NULL AND last_error_provider IS NULL AND last_error_temporary IS NULL AND last_error_timestamp_ms IS NULL)
		OR last_error_timestamp_ms IS NOT NULL
	)
);
`

const recordsReadyIndexSchema = `
CREATE INDEX IF NOT EXISTS idx_records_ready
ON records(state, next_attempt_at_ms, created_at_ms, id);
`

const recordsOperationIndexSchema = `
CREATE INDEX IF NOT EXISTS idx_records_operation_id
ON records(operation_id);
`
