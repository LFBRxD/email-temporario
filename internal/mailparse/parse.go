// Package mailparse turns raw RFC 5322 bytes into display-ready fields.
// Input is untrusted: everything here is bounded and never panics on junk.
package mailparse

import (
	"bytes"
	"io"
	"strings"
	"unicode"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
)

const (
	maxParts       = 100 // bounds CPU spent on pathological multipart bombs
	maxHeaderRunes = 512
	maxNameRunes   = 120
)

type Attachment struct {
	Filename    string // sanitised, may be empty
	ContentType string
	Data        []byte
}

type Message struct {
	From, To, Subject string
	Text, HTML        string
	Attachments       []Attachment
}

func tolerable(err error) bool {
	return message.IsUnknownCharset(err) || message.IsUnknownEncoding(err)
}

func addrs(h mail.Header, key string) string {
	l, err := h.AddressList(key)
	if err != nil && len(l) == 0 {
		return clip(h.Get(key), maxHeaderRunes)
	}
	out := make([]string, 0, len(l))
	for _, a := range l {
		out = append(out, a.String())
	}
	return clip(strings.Join(out, ", "), maxHeaderRunes)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// SafeFilename strips path separators, quotes and control characters so the
// name is safe to put in a Content-Disposition header or show to a user.
func SafeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			continue
		}
		b.WriteRune(r)
	}
	return clip(strings.Trim(b.String(), " ."), maxNameRunes)
}

// Parse never fails: on unparseable input the raw bytes become the text body.
func Parse(raw []byte) *Message {
	p := &Message{}
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && !tolerable(err) {
		p.Text = string(raw)
		return p
	}
	p.From = addrs(mr.Header, "From")
	p.To = addrs(mr.Header, "To")
	subject, _ := mr.Header.Subject()
	p.Subject = clip(subject, maxHeaderRunes)

	for i := 0; i < maxParts; i++ {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && (!tolerable(err) || part == nil) {
			break
		}
		body, _ := io.ReadAll(part.Body)
		switch h := part.Header.(type) {
		case *mail.InlineHeader:
			ct, params, _ := h.ContentType()
			switch {
			case ct == "text/plain" && params["name"] == "":
				p.Text += string(body)
			case ct == "text/html" && params["name"] == "":
				p.HTML += string(body)
			default:
				p.Attachments = append(p.Attachments, Attachment{SafeFilename(params["name"]), ct, body})
			}
		case *mail.AttachmentHeader:
			ct, _, _ := h.ContentType()
			name, _ := h.Filename()
			p.Attachments = append(p.Attachments, Attachment{SafeFilename(name), ct, body})
		}
	}
	return p
}
