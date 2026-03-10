# FEATURE.md

## Purpose

This file is the execution backlog for the base requirements of `smtp-cloud-relay`.

It is intentionally task-first. Each task is meant to be small enough to hand to one engineer, with enough implementation detail that the assignee does not need to rediscover project intent before starting.

Use this file as the source of truth for active feature work until the relay meets the base requirements in `AGENTS.md`.

## Current State Snapshot

- The relay already has:
  - SMTP auth
  - CIDR allowlisting
  - STARTTLS/SMTPS support
  - MIME parsing
  - ACS submission
  - SES submission
  - `/healthz`
  - `/readyz`
- The current tree baseline is stable:
  - `go test ./...` is expected to pass
  - `DELIVERY_MODE=ses` is supported alongside `acs` and `noop`
  - SMTP startup, readiness, shutdown, and provider error mapping are implemented
- The major missing base requirements are:
  - deterministic sender handling
  - durable spool + retry
  - idempotent / safe async delivery flow
  - real metrics

## Non-Negotiables

Copied from `AGENTS.md`:

- Never operate as an open relay. Enforce allowlists (CIDR / mTLS / auth).
- Deterministic sender handling: rewrite/validate From, enforce verified domain sender rules.
- Durable spool + retry with backoff (disk-backed optional).
- Idempotency and safe failure modes.

## Status Legend

- `planned`: ready to start, not yet claimed
- `in_progress`: actively being implemented
- `blocked`: cannot proceed because a dependency or decision is missing
- `done`: implemented and verified against acceptance criteria

## Priority Guide

- `P0`: correctness or availability issue, or hard blocker for later work
- `P1`: required for base requirements, but not the first unblocker
- `P2`: documentation or hardening work that should follow functional completion

## Task Template

Use this format when adding or revising tasks:

```md
### TASK-ID — Title
- Status: planned
- Priority: P0
- Depends On: none
- Goal:
- Create:
- Touch:
- Remove:
- Symbols:
- Acceptance:
- Implementation Notes:
```

## Recommended Execution Order

- Start with `SENDER-001` through `SENDER-003` so sender behavior is deterministic before durable async delivery is added.
- Finish `SPOOL-001` through `SPOOL-006` as one coordinated stream.
- Finish `OBS-001` and `OBS-002` after the spool exists.
- Finish `QA-001` and `DOC-001` last.

## Stabilize Current Tree

### CORE-001 — Finish SES Support And Restore A Green Tree
- Status: done
- Priority: P0
- Depends On: none
- Goal: restore a green tree and make SES a supported runtime mode alongside ACS.
- Create:
  - `internal/providers/ses/provider_test.go`
- Touch:
  - `go.mod`
  - `go.sum`
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/providers/factory.go`
  - `internal/providers/factory_test.go`
  - `internal/providers/ses/provider.go`
  - `internal/providers/httpclient/builder.go`
  - `README.md`
  - `deploy/helm/smtp-cloud-relay/values.yaml`
  - `deploy/helm/smtp-cloud-relay/templates/_helpers.tpl`
  - `deploy/helm/smtp-cloud-relay/templates/configmap.yaml`
  - `deploy/helm/smtp-cloud-relay/templates/deployment.yaml`
- Remove:
  - `DELIVERY_MODE=ses is not implemented yet` fail-fast behavior
  - placeholder language that implies SES is intentionally unsupported
- Symbols:
  - `Config.DeliveryMode`
  - `Config.Validate`
  - `providers.Build`
  - `ses.NewProvider`
- Acceptance:
  - `go.mod` and `go.sum` declare the AWS SDK and Smithy dependencies required by the SES provider
  - `DELIVERY_MODE=ses` is a valid runtime mode when SES config is present
  - `providers.Build` constructs a working SES provider instead of failing fast
  - SES config validation remains active and is covered by tests
  - README and Helm describe `acs`, `ses`, and `noop`
  - `go test ./...` passes without provider-setup failures caused by missing SES dependencies
- Implementation Notes:
  - Use the existing SES config surface already present in `internal/config/config.go` rather than inventing a second configuration path.
  - Reuse the shared outbound HTTP client builder so ACS and SES obey the same timeout, proxy, and custom CA rules where possible.
  - Add provider-level tests for:
    - provider construction
    - request mapping
    - credential configuration
    - retryability classification
  - Keep the current synchronous provider contract for SES in this task. The async provider contract migration happens later in `SPOOL-003`.

### CORE-001A — Unify Outbound Trust Configuration Across Providers
- Status: done
- Priority: P1
- Depends On: CORE-001
- Goal: make extra trusted CA configuration apply consistently to both ACS and SES instead of only ACS.
- Create:
  - `internal/providers/httpclient/builder_test.go`
- Touch:
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/providers/factory.go`
  - `internal/providers/factory_test.go`
  - `internal/providers/httpclient/builder.go`
  - `README.md`
  - `deploy/helm/smtp-cloud-relay/values.yaml`
  - `deploy/helm/smtp-cloud-relay/templates/configmap.yaml`
  - `deploy/helm/smtp-cloud-relay/templates/deployment.yaml`
  - `deploy/helm/smtp-cloud-relay/templates/secret.yaml`
- Remove:
  - provider-specific documentation that implies `ACS_TLS_CA_FILE` and `ACS_TLS_CA_PEM` affect all outbound providers
- Symbols:
  - `Config`
  - `providers.Build`
  - `httpclient.Config`
- Acceptance:
  - SES and ACS both receive the same extra trusted CA inputs when configured
  - a provider-neutral env/config surface exists for outbound trust, or the existing ACS-named envs are explicitly wired as backward-compatible aliases
  - Helm renders the outbound trust configuration in a way that works for both ACS and SES
  - tests cover file-based and inline PEM loading through the shared HTTP client builder
  - README states the exact env vars and compatibility behavior without implying unsupported behavior
- Implementation Notes:
  - Prefer provider-neutral names such as `OUTBOUND_TLS_CA_FILE` and `OUTBOUND_TLS_CA_PEM`, but preserve compatibility with the existing ACS-named variables if that migration is taken.
  - Keep the shared `httpclient` package as the single place where extra-root trust is assembled.
  - Add a factory-level test proving the SES branch passes trust config into `httpclient.Build`.

### CORE-001B — Enforce HTTPS Validation For Custom SES Endpoints
- Status: done
- Priority: P1
- Depends On: CORE-001
- Goal: prevent misconfiguration from routing SES traffic and credentials to a non-HTTPS custom endpoint.
- Create: none
- Touch:
  - `internal/providers/ses/provider.go`
  - `internal/providers/ses/provider_test.go`
  - `README.md`
- Remove:
  - acceptance of plaintext or malformed `SES_ENDPOINT` values
- Symbols:
  - `ses.NewProvider`
- Acceptance:
  - empty `SES_ENDPOINT` still means "use the AWS SDK default resolver"
  - non-empty `SES_ENDPOINT` must parse successfully and use the `https` scheme
  - non-empty `SES_ENDPOINT` must include a host
  - tests cover valid HTTPS endpoints, invalid URLs, missing hosts, and `http://` rejection
- Implementation Notes:
  - Mirror the ACS endpoint validation posture rather than relying on the AWS SDK to accept arbitrary URLs.
  - Keep this scoped to transport safety only; this task does not add provider-specific allowlists or certificate pinning.

### CORE-002 — Fix SMTP Readiness / Shutdown Race
- Status: done
- Priority: P0
- Depends On: none
- Goal: make startup readiness and shutdown deterministic.
- Create:
  - `internal/smtp/server_shutdown_test.go` if the existing integration test file becomes too crowded
- Touch:
  - `internal/smtp/server.go`
  - `internal/smtp/server_integration_test.go`
  - `cmd/relay/main.go`
- Remove: none
- Symbols:
  - `Server.Start`
  - `Server.Close`
  - add `Server.Shutdown(ctx context.Context)`
- Acceptance:
  - immediate cancel after readiness never hangs
  - `TestServerReadySignal` passes consistently
  - wrapper-managed listeners are closed directly before waiting on serve goroutines
  - `Close()` stays available as a compatibility wrapper
- Implementation Notes:
  - The current race exists because `readyCh` is closed before the serve goroutines have safely entered the `go-smtp` accept loop with listener state registered.
  - Store wrapper-owned listeners on the relay `Server` struct.
  - Add a relay-owned wait group or equivalent completion tracking so shutdown waits on the actual serve goroutines launched by `Start`.
  - `Start` should bind listeners, store them on the server, launch the serve goroutines, then signal readiness.
  - `Shutdown(ctx)` should close the wrapper-managed listeners first, then wait for the serve goroutines to exit, then honor `ctx`.
  - `Close()` should call `Shutdown` using a compatibility path, not duplicate shutdown logic.

### CORE-002A — Define SMTP Server Lifecycle Semantics
- Status: done
- Priority: P1
- Depends On:
  - CORE-002
- Goal: make `internal/smtp.Server` lifecycle behavior explicit and safe for future reuse.
- Create: none
- Touch:
  - `internal/smtp/server.go`
  - `internal/smtp/server_integration_test.go`
  - `README.md`
- Remove:
  - implicit undefined behavior when `Start()` is called more than once on the same server instance
- Symbols:
  - `Server.Start`
  - `Server.Close`
  - `Server.Ready`
  - `Server.Shutdown`
- Acceptance:
  - repeated `Start()` attempts are explicitly rejected with a stable error
  - concurrent and sequential second `Start()` calls return the same sentinel error
  - `Close()` blocking behavior is documented and covered by tests
  - readiness behavior remains correct under the chosen single-use lifecycle model
- Implementation Notes:
  - The server now explicitly enforces single-use semantics instead of leaving restart behavior undefined.
  - `Ready()` remains bound to the single allowed run and is never reset.
  - `Close()` remains a blocking compatibility API over `Shutdown(context.Background())` and now waits for `Start()` to return.

### CORE-003 — Standardize Provider Error Metadata
- Status: done
- Priority: P0
- Depends On: CORE-001
- Goal: make every outbound failure path return consistent retryability metadata.
- Create: none
- Touch:
  - `internal/email/delivery_error.go`
  - `internal/providers/acs/provider.go`
  - `internal/providers/acs/provider_test.go`
  - `internal/providers/ses/provider.go`
  - `internal/providers/ses/provider_test.go`
  - `internal/providers/noop/provider.go`
- Remove: none
- Symbols:
  - `DeliveryError`
  - `AsDeliveryError`
  - `acs.SendError`
  - `ses.SendError`
- Acceptance:
  - ACS validation errors implement `email.DeliveryError` with `Temporary() == false`
  - ACS request-construction errors implement `email.DeliveryError` with `Temporary() == false`
  - ACS transport errors implement `email.DeliveryError` with `Temporary() == true`
  - ACS `429` and `5xx` responses implement `email.DeliveryError` with `Temporary() == true`
  - ACS non-`429` `4xx` responses implement `email.DeliveryError` with `Temporary() == false`
  - SES provider construction and MIME-building failures implement `email.DeliveryError` with `Temporary() == false`
  - SES SDK transport or throttling failures implement `email.DeliveryError` with `Temporary() == true`
  - SES non-retryable API failures implement `email.DeliveryError` with `Temporary() == false`
  - tests cover both temporary and permanent cases
- Implementation Notes:
  - Do not return raw `fmt.Errorf(...)` for provider-owned failure paths once this task is complete.
  - Introduce small helpers inside the ACS and SES providers for constructing permanent vs temporary `SendError` values.
  - Preserve provider name and status code on every error path that has that information.
  - `noop` can remain success-only, but if it returns an error in any new edge case, that error must also fit the delivery error contract.

### CORE-004 — Map Delivery Errors To Correct SMTP Replies
- Status: done
- Priority: P0
- Depends On: CORE-003
- Goal: stop treating permanent provider failures as temporary SMTP failures.
- Create:
  - `internal/smtp/delivery_status.go`
  - `internal/smtp/delivery_status_test.go`
- Touch:
  - `cmd/relay/main.go`
- Remove: none
- Symbols:
  - `MapDeliveryError(err error) *smtp.SMTPError`
- Acceptance:
  - retryable delivery errors map to `451 4.3.0`
  - permanent delivery errors map to `554 5.0.0`
  - unknown internal errors still map to a temporary SMTP failure
  - the direct handler path no longer hardcodes a single `451 temporary relay failure` for every provider error
- Implementation Notes:
  - Keep the SMTP mapping logic isolated in `internal/smtp/` so it is easy to unit test.
  - Do not infer retryability from string matching in `cmd/relay/main.go`; that logic belongs in `email.DeliveryError`.
  - Continue returning `451 4.3.2` for inflight saturation; that is a separate transient relay condition.

## Sender Policy

### SENDER-001 — Make Sender Data Explicit In The Email Model
- Status: planned
- Priority: P1
- Depends On: CORE-004
- Goal: remove ambiguity between SMTP envelope sender and message-header sender.
- Create: none
- Touch:
  - `internal/email/message.go`
  - `internal/smtp/parser.go`
  - `internal/smtp/parser_test.go`
  - `internal/smtp/server_integration_test.go`
- Remove:
  - `Message.From`
- Symbols:
  - add `Message.EnvelopeFrom`
  - add `Message.HeaderFrom`
  - add `Message.ReplyTo`
- Acceptance:
  - parsing preserves envelope sender and header sender separately
  - header parsing never overwrites envelope sender
  - `Reply-To` addresses are parsed independently from `From`
  - tests cover:
    - header `From` present and valid
    - header `From` absent
    - header `From` malformed
    - MIME-encoded display names
- Implementation Notes:
  - `EnvelopeFrom` should always be the SMTP `MAIL FROM` value as accepted by the session.
  - `HeaderFrom` should be the normalized mailbox address from the `From` header when parsing succeeds; otherwise leave it empty.
  - `ReplyTo` should be a `[]string` of normalized email addresses from the `Reply-To` header.
  - `Message.To` remains envelope recipients, not header recipients.

### SENDER-002 — Add Sender Policy Config And Normalizer
- Status: planned
- Priority: P1
- Depends On: SENDER-001
- Goal: implement deterministic sender handling before the message is accepted into the relay.
- Create:
  - `internal/email/sender_policy.go`
  - `internal/email/sender_policy_test.go`
- Touch:
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `cmd/relay/main.go`
- Remove: none
- Symbols:
  - `Config.SenderPolicyMode`
  - `Config.SenderAllowedDomains`
  - `ApplySenderPolicy`
- Acceptance:
  - add env var `SENDER_POLICY_MODE` with values `rewrite` and `strict`
  - default `SENDER_POLICY_MODE` to `rewrite`
  - add env var `SENDER_ALLOWED_DOMAINS` as a comma/space/newline-separated list
  - in `rewrite` mode:
    - the relay always uses the configured provider sender as the outbound visible sender
    - `Reply-To` is set only when the original sender is syntactically valid and allowed
    - invalid or disallowed original sender does not reject the message, but reply-to is dropped
  - in `strict` mode:
    - invalid or disallowed original sender causes permanent rejection before enqueue
- Implementation Notes:
  - Domain matching should be case-insensitive.
  - If `SENDER_ALLOWED_DOMAINS` is empty, treat it as "allow any syntactically valid domain".
  - Policy selection should use this deterministic order:
    - first valid address from `ReplyTo`, if present
    - otherwise `HeaderFrom`
    - otherwise no original sender candidate
  - In `strict` mode, no original sender candidate means reject permanently.
  - Keep the policy function pure and testable; `cmd/relay/main.go` should only wire config into it.

### SENDER-003 — Extend Provider Payloads For Reply-To And Trace Headers
- Status: planned
- Priority: P1
- Depends On: SENDER-002
- Goal: preserve original sender intent consistently across ACS and SES while still sending from the verified provider sender.
- Create: none
- Touch:
  - `internal/providers/acs/provider.go`
  - `internal/providers/acs/provider_test.go`
  - `internal/providers/ses/provider.go`
  - `internal/providers/ses/provider_test.go`
- Remove: none
- Symbols:
  - add `sendRequest.ReplyTo`
  - add `sendRequest.Headers`
- Acceptance:
  - ACS JSON includes `replyTo` when sender policy allows it
  - ACS JSON includes trace headers:
    - `X-SMTP-Relay-Envelope-From`
    - `X-SMTP-Relay-Header-From`
  - SES raw MIME includes:
    - `Reply-To` when sender policy allows it
    - `X-SMTP-Relay-Envelope-From`
    - `X-SMTP-Relay-Header-From`
  - tests verify the payload shape and header contents
- Implementation Notes:
  - ACS `headers` is an object map, not a list.
  - ACS `replyTo` is an array of email-address objects.
  - The visible outbound sender must always remain the configured verified sender for the selected provider.
  - Trace headers should be included even when `replyTo` is empty, unless the value is unavailable.
  - For SES, use the existing raw MIME builder and add the headers directly into the MIME message rather than inventing a provider-specific side channel.

## Durable Spool + Idempotency

### SPOOL-001 — Introduce Filesystem Spool Types And Store
- Status: planned
- Priority: P0
- Depends On: CORE-004
- Goal: create a durable local queue before changing SMTP acceptance semantics.
- Create:
  - `internal/spool/types.go`
  - `internal/spool/store.go`
  - `internal/spool/fsstore.go`
  - `internal/spool/fsstore_test.go`
- Touch:
  - `internal/config/config.go`
  - `internal/config/config_test.go`
- Remove: none
- Symbols:
  - `Record`
  - `State`
  - `Store`
  - `FSStore`
- Acceptance:
  - spool records are persisted as one JSON file per item under state subdirectories inside `SPOOL_DIR`
  - `Record` includes:
    - `ID`
    - `Message`
    - `State`
    - `Attempt`
    - `NextAttemptAt`
    - `OperationID`
    - `OperationLocation`
    - `LastError`
    - `CreatedAt`
    - `UpdatedAt`
  - the store supports:
    - `Enqueue`
    - `ClaimReady`
    - `MarkSubmitted`
    - `MarkRetry`
    - `MarkSucceeded`
    - `MarkDeadLetter`
    - `Recover`
- Implementation Notes:
  - Define these directories under `SPOOL_DIR`:
    - `queued`
    - `working`
    - `submitted`
    - `succeeded`
    - `dead-letter`
  - Use one JSON file per record: `<state>/<id>.json`.
  - `Enqueue` should:
    - assign a new UUID
    - write a temp file in the target directory
    - `fsync` the file
    - rename it into place
    - `fsync` the directory
  - State transitions should be implemented as atomic rename-based moves within the spool root.
  - `ClaimReady` should move one eligible record from `queued` to `working`.
  - `Recover` should move stale `working` records back to `queued` and leave `submitted` records in place for polling recovery.
  - `LastError` should be a structured object, not just a string. Include message, provider, temporary flag, and timestamp.

### SPOOL-002 — Accept SMTP After Durable Enqueue, Not After Provider Send
- Status: planned
- Priority: P0
- Depends On:
  - SPOOL-001
  - SENDER-002
- Goal: make SMTP success mean "accepted into durable queue", not "provider send completed".
- Create:
  - `internal/relay/handler.go`
  - `internal/relay/handler_test.go`
- Touch:
  - `cmd/relay/main.go`
- Remove:
  - the direct provider call from the SMTP path in `cmd/relay/main.go`
- Symbols:
  - `relay.Handler`
  - `relay.Handler.HandleMessage`
- Acceptance:
  - SMTP `250` is returned only after the spool write commits
  - sender-policy permanent failures reject before enqueue
  - spool-write failures return a temporary SMTP failure
  - provider submission no longer happens on the SMTP request path
- Implementation Notes:
  - Move message-acceptance logic out of `buildMessageHandler` and into a dedicated relay package.
  - The relay handler should:
    - apply sender policy
    - generate a spool record
    - enqueue it
    - return success
  - Keep `SMTP_MAX_INFLIGHT_SENDS` as a guard on concurrent enqueue work unless a later task intentionally renames it.
  - Do not start the worker from inside the handler; wiring belongs in `cmd/relay/main.go`.

### SPOOL-003 — Replace Provider.Send With Submit + Poll
- Status: planned
- Priority: P0
- Depends On: SPOOL-001
- Goal: support both long-running and immediate-completion providers instead of synchronous send semantics.
- Create:
  - `internal/email/submission.go`
- Touch:
  - `internal/email/provider.go`
  - `internal/providers/noop/provider.go`
  - `internal/providers/ses/provider.go`
  - `internal/providers/factory.go`
- Remove:
  - `Provider.Send`
- Symbols:
  - `Provider.Submit(ctx, msg, operationID string) (SubmissionResult, error)`
  - `Provider.Poll(ctx, operationID string) (SubmissionStatus, error)`
  - `SubmissionResult`
  - `SubmissionStatus`
- Acceptance:
  - the provider contract compiles without synchronous send semantics
  - `SubmissionResult` includes provider-independent completion state so the worker can handle both ACS-style long-running operations and SES-style immediate completion
  - `noop` simulates accepted submission with immediate success
  - the contract supports:
    - long-running submit + poll providers
    - immediate-completion providers that do not normally need poll
  - provider runtime wiring in `internal/providers/factory.go` still works for `acs`, `ses`, and `noop`
- Implementation Notes:
  - `SubmissionResult` should include:
    - `OperationID`
    - `OperationLocation`
    - `RetryAfter`
    - `State`
    - `ProviderMessageID`
  - `SubmissionStatus` should include:
    - `OperationID`
    - `State`
    - `RetryAfter`
    - `ProviderMessageID`
    - optional failure metadata
  - Define operation states as lower-case internal values:
    - `running`
    - `succeeded`
    - `failed`
    - `canceled`
  - Keep the interface provider-agnostic so a future provider can implement it without ACS-specific types.
  - The worker must only call `Poll` when `Submit` returns a non-terminal state.

### SPOOL-004A — Implement ACS Submit + Poll Using Customer Operation IDs
- Status: planned
- Priority: P0
- Depends On:
  - SPOOL-003
  - SENDER-003
- Goal: make each spooled item addressable and recoverable through ACS APIs.
- Create: none
- Touch:
  - `internal/providers/acs/provider.go`
  - `internal/providers/acs/provider_test.go`
- Remove: none
- Symbols:
  - `Provider.Submit`
  - `Provider.Poll`
- Acceptance:
  - `Submit` sends the spool record ID as the ACS `Operation-Id` request header
  - `Submit` captures:
    - `id`
    - `operation-location`
    - `retry-after`
  - `Poll` calls `GET /emails/operations/{operationId}`
  - `Poll` returns one of:
    - `running`
    - `succeeded`
    - `failed`
    - `canceled`
- Implementation Notes:
  - `x-ms-client-request-id` remains a request trace identifier and does not replace `Operation-Id`.
  - Use the spool record ID as the long-running operation ID so retries and crash recovery remain idempotent.
  - Keep existing HMAC auth logic and extend it to the poll request.
  - Parse `retry-after` from both submit and poll responses when present.
  - Be explicit in code comments that ACS operation success is submission success, not final recipient delivery.

### SPOOL-004B — Implement SES Submit With Immediate Completion Semantics
- Status: planned
- Priority: P0
- Depends On:
  - SPOOL-003
  - SENDER-003
- Goal: make SES work under the shared async provider contract without inventing fake long-running behavior.
- Create: none
- Touch:
  - `internal/providers/ses/provider.go`
  - `internal/providers/ses/provider_test.go`
- Remove: none
- Symbols:
  - `Provider.Submit`
  - `Provider.Poll`
- Acceptance:
  - `Submit` sends via SES v2 and returns `State == succeeded` when SES accepts the message
  - `Submit` returns the SES `MessageId` in `ProviderMessageID`
  - retryable and permanent SES failures are classified through the shared delivery error contract
  - `Poll` is implemented for interface completeness but is not used in the normal SES path once `Submit` returns a terminal state
  - tests verify:
    - configuration set usage
    - reply-to and trace header preservation
    - temporary vs permanent error classification
- Implementation Notes:
  - SES does not need an ACS-style long-running operation workflow for the first implementation.
  - The worker should mark SES records succeeded immediately when `Submit` returns terminal success.
  - Keep the interface shape the same as ACS, but do not force SES into a fake submitted-state lifecycle.

### SPOOL-005 — Add Worker Loop, Retry Scheduling, Recovery, And Dead-Letter
- Status: planned
- Priority: P0
- Depends On:
  - SPOOL-002
  - SPOOL-004A
  - SPOOL-004B
- Goal: complete the async delivery pipeline.
- Create:
  - `internal/spool/worker.go`
  - `internal/spool/worker_test.go`
- Touch:
  - `cmd/relay/main.go`
  - `internal/relay/handler.go`
- Remove: none
- Symbols:
  - `Worker`
  - `Worker.Start`
  - `Worker.Recover`
- Acceptance:
  - the worker claims ready records
  - the worker submits queued records
  - the worker polls submitted records only for providers that return non-terminal submit results
  - retries use `DELIVERY_RETRY_ATTEMPTS` and `DELIVERY_RETRY_BASE_DELAY_MS`
  - exhausted or permanent failures move to `dead-letter`
  - restart recovery resumes queued and submitted work without losing records
- Implementation Notes:
  - The worker loop should have two phases:
    - recovery at startup
    - steady-state polling/claiming loop
  - Recovery rules:
    - move stale `working` records back to `queued`
    - keep `submitted` records as `submitted`
    - resume polling for `submitted` records after startup
  - Retry scheduling:
    - store the next eligible attempt time on the record
    - exponential backoff based on `DELIVERY_RETRY_BASE_DELAY_MS`
    - stop retrying once `Attempt >= DELIVERY_RETRY_ATTEMPTS`
  - Permanent provider failures go directly to `dead-letter`.
  - Temporary submission failures and non-terminal operation states remain retryable.
  - Immediate-completion providers skip the `submitted` state and move directly to `succeeded` on successful submit.
  - Keep the worker single-process and single-node for the first implementation.

### SPOOL-006 — Add Spool Config And Helm Persistence
- Status: planned
- Priority: P1
- Depends On: SPOOL-005
- Goal: make the spool usable in Kubernetes with durable storage.
- Create:
  - `deploy/helm/smtp-cloud-relay/templates/pvc.yaml`
- Touch:
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `deploy/helm/smtp-cloud-relay/values.yaml`
  - `deploy/helm/smtp-cloud-relay/templates/deployment.yaml`
  - `deploy/helm/smtp-cloud-relay/templates/validate.yaml`
  - `deploy/helm/smtp-cloud-relay/templates/_helpers.tpl`
- Remove: none
- Symbols:
  - `Config.SpoolDir`
  - `Config.SpoolPollIntervalMS`
- Acceptance:
  - add `SPOOL_DIR` with default `/var/lib/smtp-cloud-relay/spool`
  - add `SPOOL_POLL_INTERVAL_MS`
  - Helm creates a PVC by default in provider modes that require durable relay semantics (`acs` and `ses`)
  - Helm validation fails if:
    - `replicaCount > 1` in `acs` or `ses` mode
    - spool persistence is disabled in `acs` or `ses` mode
- Implementation Notes:
  - The first spool implementation is explicitly single-replica.
  - Add a writable volume mount for the spool directory.
  - The Helm chart should surface PVC size, storage class, and existing-claim options.
  - Validation should be implemented in template helpers so bad values fail fast at render time.

## Observability

### OBS-001 — Replace Placeholder Metrics With Real Prometheus Metrics
- Status: planned
- Priority: P1
- Depends On: SPOOL-005
- Goal: make `/metrics` operational.
- Create:
  - `internal/observability/metrics.go`
  - `internal/observability/metrics_test.go`
- Touch:
  - `internal/observability/http.go`
  - `internal/smtp/server.go`
  - `internal/spool/worker.go`
  - `internal/relay/handler.go`
- Remove:
  - the placeholder response in `internal/observability/http.go`
- Symbols:
  - `Metrics`
  - exporter wiring
- Acceptance:
  - `/metrics` exports real counters and gauges for:
    - denied sessions
    - auth failures
    - queued messages
    - enqueue failures
    - spool depth by state
    - delivery submissions
    - delivery polls
    - retries
- Implementation Notes:
  - Use the Prometheus Go client rather than hand-writing the text format.
  - Keep metric names relay-specific and stable.
  - Recommended metric names:
    - `smtp_relay_sessions_denied_total`
    - `smtp_relay_auth_failures_total`
    - `smtp_relay_enqueued_total`
    - `smtp_relay_enqueue_failures_total`
    - `smtp_relay_spool_records`
    - `smtp_relay_delivery_submissions_total`
    - `smtp_relay_delivery_polls_total`
    - `smtp_relay_delivery_retries_total`
  - Do not put high-cardinality labels on recipient or sender addresses.

### OBS-002 — Tie Readiness To Spool Recovery
- Status: planned
- Priority: P1
- Depends On:
  - SPOOL-005
  - OBS-001
- Goal: prevent the pod from reporting ready before the async system is usable.
- Create: none
- Touch:
  - `cmd/relay/main.go`
  - `internal/observability/http.go`
  - `internal/spool/worker.go`
- Remove: none
- Symbols:
  - readiness callback wiring
- Acceptance:
  - `/readyz` stays `503` until:
    - SMTP listeners are bound
    - spool recovery completes
  - `/healthz` remains a liveness signal only
- Implementation Notes:
  - Track readiness as the conjunction of:
    - SMTP server ready
    - worker recovery ready
  - Keep the readiness signal in the process root (`cmd/relay/main.go`) and pass it into the observability server as a callback.

## QA + Docs

### QA-001 — Add Cross-Cutting Integration Coverage
- Status: planned
- Priority: P1
- Depends On:
  - CORE-002
  - CORE-004
  - SENDER-003
  - SPOOL-005
  - OBS-002
- Goal: lock the new relay contract down with end-to-end tests.
- Create:
  - `internal/relay/e2e_test.go`
- Touch:
  - `internal/providers/acs/provider_test.go`
  - `internal/smtp/server_integration_test.go`
- Remove: none
- Symbols:
  - test helpers only
- Acceptance:
  - test matrix covers:
    - enqueue-before-ack behavior
    - restart recovery after a queued-but-unsent message
    - temporary ACS failure with retry
    - permanent ACS failure to dead-letter
    - temporary SES failure with retry
    - permanent SES failure to dead-letter
    - sender rewrite mode
    - sender strict rejection
    - readiness blocked until recovery completes
- Implementation Notes:
  - Prefer temp directories and fake providers over live network calls.
  - Use a fake provider that implements `Submit` and `Poll` so retry and dead-letter behavior can be driven deterministically.
  - Keep ACS provider tests focused on request/response mapping; keep relay behavior in relay/spool integration tests.

### DOC-001 — Update README And Deployment Guidance
- Status: planned
- Priority: P2
- Depends On:
  - CORE-001
  - SENDER-003
  - SPOOL-006
  - OBS-002
- Goal: make the docs match the actual relay contract.
- Create: none
- Touch:
  - `README.md`
  - `Makefile`
- Remove:
  - `/metrics` placeholder language
- Symbols:
  - documentation only
- Acceptance:
  - README explains:
    - ACS and SES runtime modes
    - sender-policy env vars
    - spool env vars
    - durable acceptance semantics
    - metrics
    - Helm persistence requirements
  - `make test` docs match current repository behavior
- Implementation Notes:
  - Update the "What It Does", config, and Helm sections in README.
  - If `Makefile` behavior changes during implementation, document the new developer workflow instead of describing an outdated one.

## Cross-Cutting Acceptance Criteria

- `go test ./...` passes in a clean environment.
- A message in `acs` or `ses` mode is acknowledged to the SMTP client only after durable local enqueue.
- A process restart after enqueue but before submission does not lose the message.
- A provider temporary failure is retried with backoff.
- A provider permanent failure is not retried forever and ends in dead-letter.
- The visible outbound sender is always the configured verified sender for the selected provider.
- Original sender information is preserved deterministically through `replyTo` and relay trace headers when allowed for both ACS and SES.
- `/metrics` exports real telemetry and `/readyz` reflects async recovery state.

## Assumptions And Defaults

- ACS and SES are both in-scope real outbound providers.
- The first durable spool implementation is filesystem-based and single-replica only.
- PVC-backed storage is the default deployment mode for provider-backed relay operation (`acs` and `ses`) in Helm.
- ACS uses a long-running submit + poll flow; SES uses immediate submit completion for the first implementation.
- ACS operation polling stops at the documented send-operation terminal state.
- Final recipient delivery events are future work and can later use Azure Monitor or Event Grid.

## Deferred / Future

### FUTURE-001 — Add Provider Conformance Tests For Future Providers
- Status: blocked
- Priority: P2
- Depends On:
  - SPOOL-005
  - QA-001
- Goal: make it harder to add a new provider that silently breaks retry, dead-letter, sender policy, or readiness semantics.
- Create:
  - future provider conformance test helpers
- Touch:
  - future provider test suites
- Remove: none
- Symbols:
  - future provider contract test harness
- Acceptance:
  - adding a new provider requires it to pass the same contract checks for submit behavior, poll behavior, retryability, and sender preservation
- Implementation Notes:
  - This becomes more valuable now that the relay supports more than one real outbound provider.

### FUTURE-002 — Add Final Delivery Event Correlation
- Status: blocked
- Priority: P2
- Depends On:
  - SPOOL-004
  - OBS-001
- Goal: correlate ACS submission success with downstream delivery or bounce events.
- Create:
  - future event-ingestion package when the team decides on Azure Monitor or Event Grid
- Touch:
  - spool record schema
  - observability pipeline
- Remove: none
- Symbols:
  - future event correlator
- Acceptance:
  - the relay can connect final delivery events back to the original spool record or ACS operation
- Implementation Notes:
  - ACS submit/poll endpoints confirm long-running operation status, not final recipient delivery.
  - This is intentionally not part of the first spool implementation.

## Source Notes

- ACS send request shape, `Operation-Id`, `replyTo`, `headers`, `Operation-Location`, and `retry-after` are documented in Microsoft Learn for ACS Email REST API version `2023-03-31`.
- ACS send-result polling for `GET /emails/operations/{operationId}` terminal and non-terminal states is also documented in Microsoft Learn for the same API version.
