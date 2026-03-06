# AGENTS.md — smtp-cloud-relay

## Mission
Provide an SMTP endpoint (for apps like Jira/Confluence) that translates messages into Azure Communication Services Email API calls, with GCC High constraints in mind.

## Non-negotiables
- Never operate as an open relay. Enforce allowlists (CIDR / mTLS / auth).
- Deterministic sender handling: rewrite/validate From, enforce verified domain sender rules.
- Durable spool + retry with backoff (disk-backed optional).
- Idempotency and safe failure modes.

## Dev defaults
- SMTP listen: 0.0.0.0:2525
- HTTP health/metrics: 0.0.0.0:8080
- Local testing uses `swaks`.

## Interfaces (initial)
- SMTP IN: minimal ESMTP, STARTTLS optional (feature-gated)
- OUT: ACS Email (REST/SDK)
- Observability: /healthz, /readyz, /metrics