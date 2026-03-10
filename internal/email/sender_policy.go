package email

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
)

const (
	SenderPolicyReasonNoOriginalSender       = "no_original_sender"
	SenderPolicyReasonInvalidOriginalSender  = "invalid_original_sender"
	SenderPolicyReasonDisallowedSenderDomain = "disallowed_sender_domain"
)

type SenderPolicyMode string

const (
	SenderPolicyRewrite SenderPolicyMode = "rewrite"
	SenderPolicyStrict  SenderPolicyMode = "strict"
)

type SenderPolicyOptions struct {
	Mode                  SenderPolicyMode
	AllowedDomainPatterns []string
	VerifiedSender        string
}

type SenderPolicy struct {
	mode           SenderPolicyMode
	verifiedSender string
	matchers       []senderDomainMatcher
}

type SenderPolicyResult struct {
	Message          Message
	OriginalSender   string
	EffectiveReplyTo []string
	DecisionReason   string
}

type SenderPolicyError struct {
	Reason string
}

func (e *SenderPolicyError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("sender policy rejected message: %s", e.Reason)
}

func AsSenderPolicyError(err error) (*SenderPolicyError, bool) {
	var policyErr *SenderPolicyError
	if !errors.As(err, &policyErr) {
		return nil, false
	}
	return policyErr, true
}

func NewSenderPolicy(opts SenderPolicyOptions) (SenderPolicy, error) {
	mode := SenderPolicyMode(strings.ToLower(strings.TrimSpace(string(opts.Mode))))
	switch mode {
	case SenderPolicyRewrite, SenderPolicyStrict:
	default:
		return SenderPolicy{}, fmt.Errorf("unsupported sender policy mode %q", opts.Mode)
	}

	matchers := make([]senderDomainMatcher, 0, len(opts.AllowedDomainPatterns))
	for _, pattern := range opts.AllowedDomainPatterns {
		matcher, err := compileSenderDomainMatcher(pattern)
		if err != nil {
			return SenderPolicy{}, err
		}
		if matcher == nil {
			continue
		}
		matchers = append(matchers, matcher)
	}

	return SenderPolicy{
		mode:           mode,
		verifiedSender: strings.TrimSpace(opts.VerifiedSender),
		matchers:       matchers,
	}, nil
}

func ApplySenderPolicy(msg Message, policy SenderPolicy) (SenderPolicyResult, error) {
	result := SenderPolicyResult{Message: msg}

	candidate := selectOriginalSender(msg)
	result.OriginalSender = candidate

	if candidate == "" {
		return applyPolicyDecision(result, policy.mode, SenderPolicyReasonNoOriginalSender)
	}

	domain, ok := senderDomain(candidate)
	if !ok {
		return applyPolicyDecision(result, policy.mode, SenderPolicyReasonInvalidOriginalSender)
	}

	if !policy.allowedDomain(domain) {
		return applyPolicyDecision(result, policy.mode, SenderPolicyReasonDisallowedSenderDomain)
	}

	result.Message.ReplyTo = []string{candidate}
	result.EffectiveReplyTo = []string{candidate}
	result.DecisionReason = ""
	return result, nil
}

func selectOriginalSender(msg Message) string {
	if len(msg.ReplyTo) > 0 {
		return strings.TrimSpace(msg.ReplyTo[0])
	}
	return strings.TrimSpace(msg.HeaderFrom)
}

func senderDomain(address string) (string, bool) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(address))
	if err != nil {
		return "", false
	}

	at := strings.LastIndex(parsed.Address, "@")
	if at <= 0 || at == len(parsed.Address)-1 {
		return "", false
	}

	domain := strings.TrimSpace(parsed.Address[at+1:])
	if domain == "" {
		return "", false
	}

	return strings.ToLower(domain), true
}

func (p SenderPolicy) allowedDomain(domain string) bool {
	if len(p.matchers) == 0 {
		return true
	}

	for _, matcher := range p.matchers {
		if matcher.Match(domain) {
			return true
		}
	}

	return false
}

func applyPolicyDecision(result SenderPolicyResult, mode SenderPolicyMode, reason string) (SenderPolicyResult, error) {
	result.Message.ReplyTo = nil
	result.EffectiveReplyTo = nil
	result.DecisionReason = reason

	if mode == SenderPolicyStrict {
		return result, &SenderPolicyError{Reason: reason}
	}
	return result, nil
}

type senderDomainMatcher interface {
	Match(domain string) bool
}

type exactDomainMatcher struct {
	domain string
}

func (m exactDomainMatcher) Match(domain string) bool {
	return strings.EqualFold(domain, m.domain)
}

type regexDomainMatcher struct {
	pattern *regexp.Regexp
}

func (m regexDomainMatcher) Match(domain string) bool {
	return m.pattern.MatchString(domain)
}

type singleLabelGlobMatcher struct {
	suffix string
}

func (m singleLabelGlobMatcher) Match(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || strings.EqualFold(domain, m.suffix) {
		return false
	}

	parts := strings.Split(domain, ".")
	suffixParts := strings.Split(m.suffix, ".")
	if len(parts) != len(suffixParts)+1 {
		return false
	}

	return strings.Join(parts[1:], ".") == m.suffix
}

func compileSenderDomainMatcher(raw string) (senderDomainMatcher, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	switch {
	case strings.HasPrefix(strings.ToLower(raw), "re:"):
		expr := strings.TrimSpace(raw[3:])
		if expr == "" {
			return nil, fmt.Errorf("sender allowed domain regex cannot be empty")
		}
		re, err := regexp.Compile("(?i)^(?:" + expr + ")$")
		if err != nil {
			return nil, fmt.Errorf("compile sender allowed domain regex %q: %w", raw, err)
		}
		return regexDomainMatcher{pattern: re}, nil
	case strings.HasPrefix(strings.ToLower(raw), "glob:"):
		pattern := strings.TrimSpace(raw[5:])
		suffix, err := compileSingleLabelGlob(pattern)
		if err != nil {
			return nil, fmt.Errorf("compile sender allowed domain glob %q: %w", raw, err)
		}
		return singleLabelGlobMatcher{suffix: suffix}, nil
	default:
		domain, err := normalizeExactDomain(raw)
		if err != nil {
			return nil, fmt.Errorf("compile sender allowed exact domain %q: %w", raw, err)
		}
		return exactDomainMatcher{domain: domain}, nil
	}
}

func compileSingleLabelGlob(pattern string) (string, error) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return "", fmt.Errorf("glob pattern cannot be empty")
	}
	if !strings.HasPrefix(pattern, "*.") {
		return "", fmt.Errorf("glob pattern must start with *.")
	}
	if strings.Count(pattern, "*") != 1 {
		return "", fmt.Errorf("glob pattern may contain only one wildcard")
	}

	suffix := strings.TrimPrefix(pattern, "*.")
	if suffix == "" {
		return "", fmt.Errorf("glob pattern suffix cannot be empty")
	}
	if _, err := normalizeExactDomain(suffix); err != nil {
		return "", err
	}

	return suffix, nil
}

func normalizeExactDomain(domain string) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return "", fmt.Errorf("domain cannot be empty")
	}
	if strings.Contains(domain, "*") {
		return "", fmt.Errorf("exact domain cannot contain wildcard")
	}
	if strings.Contains(domain, ":") {
		return "", fmt.Errorf("exact domain cannot contain matcher prefix syntax")
	}

	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("domain must contain at least one dot")
	}
	for _, label := range labels {
		if err := validateDomainLabel(label); err != nil {
			return "", err
		}
	}

	return domain, nil
}

func validateDomainLabel(label string) error {
	if label == "" {
		return fmt.Errorf("domain label cannot be empty")
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return fmt.Errorf("domain label %q cannot start or end with '-'", label)
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return fmt.Errorf("domain label %q contains unsupported character %q", label, r)
		}
	}
	return nil
}
