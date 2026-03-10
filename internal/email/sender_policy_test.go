package email

import "testing"

func TestApplySenderPolicyReplyToWinsOverHeaderFrom(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{
		Mode:                  SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"reply.example.com"},
	})

	msg := Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "header@example.com",
		ReplyTo:      []string{"reply@reply.example.com", "other@reply.example.com"},
	}

	result, err := ApplySenderPolicy(msg, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() error: %v", err)
	}
	if result.OriginalSender != "reply@reply.example.com" {
		t.Fatalf("unexpected original sender: %q", result.OriginalSender)
	}
	if len(result.Message.ReplyTo) != 1 || result.Message.ReplyTo[0] != "reply@reply.example.com" {
		t.Fatalf("unexpected effective reply-to: %#v", result.Message.ReplyTo)
	}
	if result.Message.EnvelopeFrom != "envelope@example.com" {
		t.Fatalf("unexpected envelope from: %q", result.Message.EnvelopeFrom)
	}
	if result.Message.HeaderFrom != "header@example.com" {
		t.Fatalf("unexpected header from: %q", result.Message.HeaderFrom)
	}
}

func TestApplySenderPolicyDoesNotFallBackFromDisallowedReplyTo(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{
		Mode:                  SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"header.example.com"},
	})

	msg := Message{
		HeaderFrom: "sender@header.example.com",
		ReplyTo:    []string{"reply@blocked.example.com"},
	}

	result, err := ApplySenderPolicy(msg, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() error: %v", err)
	}
	if result.OriginalSender != "reply@blocked.example.com" {
		t.Fatalf("unexpected original sender: %q", result.OriginalSender)
	}
	if len(result.Message.ReplyTo) != 0 {
		t.Fatalf("expected reply-to to be cleared, got %#v", result.Message.ReplyTo)
	}
}

func TestApplySenderPolicyAllowsAnyValidCandidateWhenPatternsEmpty(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{Mode: SenderPolicyRewrite})

	result, err := ApplySenderPolicy(Message{HeaderFrom: "Header@Example.COM"}, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() error: %v", err)
	}
	if len(result.EffectiveReplyTo) != 1 || result.EffectiveReplyTo[0] != "Header@Example.COM" {
		t.Fatalf("unexpected effective reply-to: %#v", result.EffectiveReplyTo)
	}
}

func TestApplySenderPolicyMatchesExactDomainCaseInsensitive(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{
		Mode:                  SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"example.com"},
	})

	allowed, err := ApplySenderPolicy(Message{HeaderFrom: "sender@Example.COM"}, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() allowed error: %v", err)
	}
	if len(allowed.EffectiveReplyTo) != 1 {
		t.Fatalf("expected allowed reply-to, got %#v", allowed.EffectiveReplyTo)
	}

	disallowed, err := ApplySenderPolicy(Message{HeaderFrom: "sender@notexample.com"}, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() disallowed error: %v", err)
	}
	if len(disallowed.EffectiveReplyTo) != 0 {
		t.Fatalf("expected disallowed reply-to to be dropped, got %#v", disallowed.EffectiveReplyTo)
	}
}

func TestApplySenderPolicyMatchesGlobSingleLabelSubdomainOnly(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{
		Mode:                  SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"glob:*.example.com"},
	})

	allowed, err := ApplySenderPolicy(Message{HeaderFrom: "sender@mail.example.com"}, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() allowed error: %v", err)
	}
	if len(allowed.EffectiveReplyTo) != 1 {
		t.Fatalf("expected allowed reply-to, got %#v", allowed.EffectiveReplyTo)
	}

	root, err := ApplySenderPolicy(Message{HeaderFrom: "sender@example.com"}, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() root error: %v", err)
	}
	if len(root.EffectiveReplyTo) != 0 {
		t.Fatalf("expected root domain to be dropped, got %#v", root.EffectiveReplyTo)
	}

	deep, err := ApplySenderPolicy(Message{HeaderFrom: "sender@a.b.example.com"}, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() deep error: %v", err)
	}
	if len(deep.EffectiveReplyTo) != 0 {
		t.Fatalf("expected deep subdomain to be dropped, got %#v", deep.EffectiveReplyTo)
	}
}

func TestApplySenderPolicyMatchesRegexCaseInsensitiveAndFullDomain(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{
		Mode:                  SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"re:(?:.+\\.)?example\\.com"},
	})

	allowed, err := ApplySenderPolicy(Message{HeaderFrom: "sender@sub.EXAMPLE.com"}, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() allowed error: %v", err)
	}
	if len(allowed.EffectiveReplyTo) != 1 {
		t.Fatalf("expected allowed reply-to, got %#v", allowed.EffectiveReplyTo)
	}

	disallowed, err := ApplySenderPolicy(Message{HeaderFrom: "sender@example.net"}, policy)
	if err != nil {
		t.Fatalf("ApplySenderPolicy() disallowed error: %v", err)
	}
	if len(disallowed.EffectiveReplyTo) != 0 {
		t.Fatalf("expected disallowed reply-to to be dropped, got %#v", disallowed.EffectiveReplyTo)
	}
}

func TestApplySenderPolicyRewriteClearsReplyToForMissingOrMalformedCandidate(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{Mode: SenderPolicyRewrite})

	tests := []Message{
		{},
		{HeaderFrom: "not-an-address"},
		{ReplyTo: []string{"also-not-an-address"}},
	}

	for _, msg := range tests {
		result, err := ApplySenderPolicy(msg, policy)
		if err != nil {
			t.Fatalf("ApplySenderPolicy() error: %v", err)
		}
		if len(result.EffectiveReplyTo) != 0 {
			t.Fatalf("expected empty effective reply-to, got %#v", result.EffectiveReplyTo)
		}
		if result.DecisionReason != SenderPolicyReasonInvalidOriginalSender && result.DecisionReason != SenderPolicyReasonNoOriginalSender {
			t.Fatalf("unexpected decision reason: %q", result.DecisionReason)
		}
	}
}

func TestApplySenderPolicyStrictRejectsWithoutCandidate(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{Mode: SenderPolicyStrict})

	_, err := ApplySenderPolicy(Message{}, policy)
	policyErr, ok := AsSenderPolicyError(err)
	if !ok {
		t.Fatalf("expected SenderPolicyError, got %T", err)
	}
	if policyErr.Reason != SenderPolicyReasonNoOriginalSender {
		t.Fatalf("unexpected rejection reason: %q", policyErr.Reason)
	}
}

func TestApplySenderPolicyStrictRejectsDisallowedDomain(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{
		Mode:                  SenderPolicyStrict,
		AllowedDomainPatterns: []string{"allowed.example.com"},
	})

	_, err := ApplySenderPolicy(Message{HeaderFrom: "sender@blocked.example.com"}, policy)
	policyErr, ok := AsSenderPolicyError(err)
	if !ok {
		t.Fatalf("expected SenderPolicyError, got %T", err)
	}
	if policyErr.Reason != SenderPolicyReasonDisallowedSenderDomain {
		t.Fatalf("unexpected rejection reason: %q", policyErr.Reason)
	}
}

func TestApplySenderPolicyStrictRejectsMalformedChosenSender(t *testing.T) {
	t.Parallel()

	policy := mustSenderPolicy(t, SenderPolicyOptions{Mode: SenderPolicyStrict})

	_, err := ApplySenderPolicy(Message{ReplyTo: []string{"not-an-address"}}, policy)
	policyErr, ok := AsSenderPolicyError(err)
	if !ok {
		t.Fatalf("expected SenderPolicyError, got %T", err)
	}
	if policyErr.Reason != SenderPolicyReasonInvalidOriginalSender {
		t.Fatalf("unexpected rejection reason: %q", policyErr.Reason)
	}
}

func TestNewSenderPolicyRejectsInvalidRegex(t *testing.T) {
	t.Parallel()

	_, err := NewSenderPolicy(SenderPolicyOptions{
		Mode:                  SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"re:("},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestNewSenderPolicyRejectsInvalidGlob(t *testing.T) {
	t.Parallel()

	_, err := NewSenderPolicy(SenderPolicyOptions{
		Mode:                  SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"glob:*.*.example.com"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func mustSenderPolicy(t *testing.T, opts SenderPolicyOptions) SenderPolicy {
	t.Helper()

	policy, err := NewSenderPolicy(opts)
	if err != nil {
		t.Fatalf("NewSenderPolicy() error: %v", err)
	}
	return policy
}
