// Package smtpd is the receive-only SMTP front end. It never relays and never
// sends mail (so it cannot be used for spam or backscatter): a recipient is
// accepted only if its inbox exists, or, when auto-create is enabled, can be
// created on the spot for a valid name under our own domain.
package smtpd

import (
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-smtp"

	"tempmail/internal/inbox"
	"tempmail/internal/mailparse"
)

// Recipients is what the server needs from the mailbox store.
type Recipients interface {
	Exists(addr string) bool
	Create(name string) (inbox.View, error)
	Deliver(addr string, m *inbox.Message) error
}

// Limiter decides whether key may perform one more action right now.
type Limiter interface{ Allow(key string) bool }

type Options struct {
	Domain            string
	MaxMsgBytes       int
	MaxConns          int
	MaxConnsPerIP     int
	MaxConcurrentData int
	// AutoCreate, when non-nil, lets mail for a not-yet-existing inbox create
	// it (limited per sender IP by this limiter). nil = unknown recipients get 550.
	AutoCreate Limiter
}

type Server struct {
	srv  *smtp.Server
	opts Options
}

func New(rcpt Recipients, o Options) *Server {
	b := &backend{rcpt: rcpt, opts: o, sem: make(chan struct{}, o.MaxConcurrentData)}
	s := smtp.NewServer(b)
	s.Domain = o.Domain
	s.MaxMessageBytes = int64(o.MaxMsgBytes)
	s.MaxRecipients = 10
	s.ReadTimeout = 30 * time.Second
	s.WriteTimeout = 30 * time.Second
	s.AllowInsecureAuth = false
	return &Server{srv: s, opts: o}
}

// Serve accepts connections on l (wrapped with connection limits) until Close.
func (s *Server) Serve(l net.Listener) error {
	return s.srv.Serve(newLimitListener(l, s.opts.MaxConns, s.opts.MaxConnsPerIP))
}

func (s *Server) Close() error { return s.srv.Close() }

type backend struct {
	rcpt Recipients
	opts Options
	sem  chan struct{} // bounds messages held in memory at once
}

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &session{b: b, ip: remoteIP(c.Conn())}, nil
}

type session struct {
	b  *backend
	ip string
	to []string
}

func (s *session) Reset()                                   { s.to = nil }
func (s *session) Logout() error                            { return nil }
func (s *session) Mail(_ string, _ *smtp.MailOptions) error { return nil }

var errNoUser = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	to = strings.ToLower(to)
	if !s.b.rcpt.Exists(to) {
		if err := s.autoCreate(to); err != nil {
			return err
		}
	}
	s.to = append(s.to, to)
	return nil
}

// autoCreate makes the inbox for `to` if the feature is on and the address is
// a valid name under our domain. Anything else is refused: we are no relay.
func (s *session) autoCreate(to string) error {
	if s.b.opts.AutoCreate == nil {
		return errNoUser
	}
	name, ok := strings.CutSuffix(to, "@"+s.b.opts.Domain)
	if !ok || !inbox.ValidName(name) {
		return errNoUser
	}
	if !s.b.opts.AutoCreate.Allow(s.ip) {
		return &smtp.SMTPError{Code: 450, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "too many new mailboxes, try again later"}
	}
	switch _, err := s.b.rcpt.Create(name); {
	case err == nil, errors.Is(err, inbox.ErrTaken): // taken = created concurrently
		return nil
	case errors.Is(err, inbox.ErrCapacity):
		return &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 3, 1}, Message: "mailbox capacity reached, try again later"}
	default:
		return errNoUser
	}
}

func (s *session) Data(r io.Reader) error {
	s.b.sem <- struct{}{}
	defer func() { <-s.b.sem }()

	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	// ReadAll over-allocates; keep only the exact bytes so the store's
	// accounting matches real memory use.
	raw = append(make([]byte, 0, len(raw)), raw...)
	p := mailparse.Parse(raw)

	delivered := 0
	for _, to := range s.to {
		m := &inbox.Message{From: p.From, Subject: p.Subject, Received: time.Now(), NAttach: len(p.Attachments), Raw: raw}
		err := s.b.rcpt.Deliver(to, m)
		if err == nil {
			delivered++
		} else if !errors.Is(err, inbox.ErrNoInbox) && !errors.Is(err, inbox.ErrTooLarge) {
			return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "temporary failure"}
		}
	}
	if delivered == 0 {
		// Inbox expired between RCPT and DATA, or message exceeds the store.
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 2, 1}, Message: "mailbox unavailable"}
	}
	return nil
}
