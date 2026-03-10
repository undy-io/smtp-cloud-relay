package email

import (
	"reflect"
	"testing"
)

func TestSenderTraceHeaders(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want []TraceHeader
	}{
		{
			name: "both present",
			msg: Message{
				EnvelopeFrom: "envelope@example.com",
				HeaderFrom:   "header@example.com",
			},
			want: []TraceHeader{
				{Name: TraceHeaderEnvelopeFrom, Value: "envelope@example.com"},
				{Name: TraceHeaderHeaderFrom, Value: "header@example.com"},
			},
		},
		{
			name: "only envelope sender",
			msg: Message{
				EnvelopeFrom: "envelope@example.com",
			},
			want: []TraceHeader{
				{Name: TraceHeaderEnvelopeFrom, Value: "envelope@example.com"},
			},
		},
		{
			name: "only header sender",
			msg: Message{
				HeaderFrom: "header@example.com",
			},
			want: []TraceHeader{
				{Name: TraceHeaderHeaderFrom, Value: "header@example.com"},
			},
		},
		{
			name: "both absent",
			msg:  Message{},
			want: []TraceHeader{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SenderTraceHeaders(tc.msg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SenderTraceHeaders() = %#v, want %#v", got, tc.want)
			}
		})
	}
}
