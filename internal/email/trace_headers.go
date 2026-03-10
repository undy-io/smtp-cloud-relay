package email

import "strings"

const (
	TraceHeaderEnvelopeFrom = "X-SMTP-Relay-Envelope-From"
	TraceHeaderHeaderFrom   = "X-SMTP-Relay-Header-From"
)

type TraceHeader struct {
	Name  string
	Value string
}

func SenderTraceHeaders(msg Message) []TraceHeader {
	headers := make([]TraceHeader, 0, 2)

	if value := strings.TrimSpace(msg.EnvelopeFrom); value != "" {
		headers = append(headers, TraceHeader{Name: TraceHeaderEnvelopeFrom, Value: value})
	}
	if value := strings.TrimSpace(msg.HeaderFrom); value != "" {
		headers = append(headers, TraceHeader{Name: TraceHeaderHeaderFrom, Value: value})
	}

	return headers
}

func SenderTraceHeaderMap(msg Message) map[string]string {
	headers := SenderTraceHeaders(msg)
	if len(headers) == 0 {
		return nil
	}

	out := make(map[string]string, len(headers))
	for _, header := range headers {
		out[header.Name] = header.Value
	}
	return out
}
