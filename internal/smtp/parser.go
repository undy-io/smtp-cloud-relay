package smtp

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

func ParseMessage(r io.Reader, envelopeFrom string, envelopeTo []string) (email.Message, error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return email.Message{}, fmt.Errorf("read smtp message: %w", err)
	}

	result := email.Message{
		EnvelopeFrom: strings.TrimSpace(envelopeFrom),
		HeaderFrom:   parseHeaderAddress(msg.Header.Get("From")),
		ReplyTo:      parseHeaderAddressList(msg.Header.Get("Reply-To")),
		To:           normalizeAddresses(envelopeTo),
		Subject:      decodeHeader(msg.Header.Get("Subject")),
	}

	contentType := msg.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}

	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return email.Message{}, fmt.Errorf("multipart message missing boundary")
		}
		mr := multipart.NewReader(msg.Body, boundary)
		if err := parseMultipart(mr, &result); err != nil {
			return email.Message{}, err
		}
		return result, nil
	}

	bodyBytes, err := io.ReadAll(msg.Body)
	if err != nil {
		return email.Message{}, fmt.Errorf("read message body: %w", err)
	}
	decodedBody, err := decodeTransferEncoding(msg.Header.Get("Content-Transfer-Encoding"), bodyBytes)
	if err != nil {
		return email.Message{}, err
	}

	if strings.EqualFold(mediaType, "text/html") {
		result.HTMLBody = string(decodedBody)
	} else {
		result.TextBody = string(decodedBody)
	}

	return result, nil
}

func parseMultipart(mr *multipart.Reader, out *email.Message) error {
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read mime part: %w", err)
		}

		contentType := part.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			mediaType = "application/octet-stream"
		}

		if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
			boundary := params["boundary"]
			if boundary == "" {
				continue
			}
			nested := multipart.NewReader(part, boundary)
			if err := parseMultipart(nested, out); err != nil {
				return err
			}
			continue
		}

		partBytes, err := io.ReadAll(part)
		if err != nil {
			return fmt.Errorf("read mime part body: %w", err)
		}

		decoded, err := decodeTransferEncoding(part.Header.Get("Content-Transfer-Encoding"), partBytes)
		if err != nil {
			return err
		}

		disposition, dispositionParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		filename := strings.TrimSpace(part.FileName())
		if filename == "" {
			filename = strings.TrimSpace(dispositionParams["filename"])
		}

		isAttachment := strings.EqualFold(disposition, "attachment") || filename != ""
		if isAttachment {
			if mediaType == "" || mediaType == "application/octet-stream" {
				mediaType = "application/octet-stream"
			}
			out.Attachments = append(out.Attachments, email.Attachment{
				Filename:    filename,
				ContentType: mediaType,
				Data:        decoded,
			})
			continue
		}

		switch {
		case strings.EqualFold(mediaType, "text/plain"):
			if out.TextBody == "" {
				out.TextBody = string(decoded)
			} else {
				out.TextBody += "\n" + string(decoded)
			}
		case strings.EqualFold(mediaType, "text/html"):
			if out.HTMLBody == "" {
				out.HTMLBody = string(decoded)
			} else {
				out.HTMLBody += "\n" + string(decoded)
			}
		}
	}
}

func decodeTransferEncoding(encoding string, data []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "7bit", "8bit", "binary":
		return data, nil
	case "base64":
		cleaned := bytes.ReplaceAll(data, []byte("\n"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\r"), nil)
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(cleaned)))
		n, err := base64.StdEncoding.Decode(decoded, cleaned)
		if err != nil {
			return nil, fmt.Errorf("decode base64 mime part: %w", err)
		}
		return decoded[:n], nil
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data)))
		if err != nil {
			return nil, fmt.Errorf("decode quoted-printable mime part: %w", err)
		}
		return decoded, nil
	default:
		return data, nil
	}
}

func normalizeAddresses(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseHeaderAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	addr, err := mail.ParseAddress(value)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(addr.Address)
}

func parseHeaderAddressList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	addrs, err := mail.ParseAddressList(value)
	if err != nil {
		return nil
	}

	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		normalized := strings.TrimSpace(addr.Address)
		if normalized != "" {
			out = append(out, normalized)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func decodeHeader(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}

	decoder := mime.WordDecoder{}
	decoded, err := decoder.DecodeHeader(v)
	if err != nil {
		return v
	}
	return decoded
}
