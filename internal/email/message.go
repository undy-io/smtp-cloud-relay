package email

// Attachment represents a MIME attachment decoded from an SMTP message.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Message is the provider-agnostic email model.
type Message struct {
	From        string
	To          []string
	Subject     string
	TextBody    string
	HTMLBody    string
	Attachments []Attachment
}
