package mailparse

import (
	"strings"
	"testing"
)

const multipart = "From: Ana <ana@x.com>\r\nTo: a@mail.test\r\nSubject: Ola\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
	"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\ncorpo\r\n" +
	"--b\r\nContent-Type: text/html\r\n\r\n<p>oi</p>\r\n" +
	"--b\r\nContent-Type: application/pdf; name=a.pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--b--\r\n"

func TestParseMultipart(t *testing.T) {
	p := Parse([]byte(multipart))
	if p.Subject != "Ola" || !strings.Contains(p.From, "ana@x.com") {
		t.Fatalf("headers: %+v", p)
	}
	if strings.TrimSpace(p.Text) != "corpo" || strings.TrimSpace(p.HTML) != "<p>oi</p>" {
		t.Fatalf("bodies: %q / %q", p.Text, p.HTML)
	}
	if len(p.Attachments) != 1 || string(p.Attachments[0].Data) != "Hello" || p.Attachments[0].Filename != "a.pdf" {
		t.Fatalf("attachments: %+v", p.Attachments)
	}
}

func TestParseEncodedSubjectAndCharset(t *testing.T) {
	raw := "From: a@x.com\r\nSubject: =?UTF-8?B?w6dhdsOp?=\r\nContent-Type: text/plain; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\ncaf=E9\r\n"
	p := Parse([]byte(raw))
	if p.Subject != "çavé" || strings.TrimSpace(p.Text) != "café" {
		t.Fatalf("got %q / %q", p.Subject, p.Text)
	}
}

func TestParseGarbageDoesNotPanic(t *testing.T) {
	for _, in := range []string{"", "\x00\x01\x02", "not a mail", strings.Repeat("A", 100000),
		"Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\n--x\r\n--x"} {
		_ = Parse([]byte(in))
	}
}

func TestPartCountIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("Content-Type: multipart/mixed; boundary=b\r\n\r\n")
	for i := 0; i < 5000; i++ {
		b.WriteString("--b\r\nContent-Type: text/plain\r\n\r\nx\r\n")
	}
	b.WriteString("--b--\r\n")
	if got := len(Parse([]byte(b.String())).Text); got > maxParts*3 {
		t.Fatalf("parsed %d bytes of text, part cap not applied", got)
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":       "etcpasswd",
		"a\r\nb.txt":             "ab.txt",
		`ev"il\name.exe`:         "evilname.exe",
		"  .hidden. ":            "hidden",
		"":                       "",
		strings.Repeat("é", 500): strings.Repeat("é", maxNameRunes),
	}
	for in, want := range cases {
		if got := SafeFilename(in); got != want {
			t.Errorf("SafeFilename(%.20q) = %.30q, want %.30q", in, got, want)
		}
	}
}
