// Package inbox is the in-memory mailbox store: short-lived inboxes with a
// fixed TTL, bounded memory and oldest-first rotation.
package inbox

import (
	"container/list"
	"context"
	"crypto/rand"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrBadName  = errors.New("invalid name")
	ErrTaken    = errors.New("address already in use")
	ErrCapacity = errors.New("inbox capacity reached")
	ErrTooLarge = errors.New("message larger than store capacity")
	ErrNoInbox  = errors.New("no such inbox")
)

// 32 symbols (a power of two) so that byte%32 has no modulo bias.
const (
	alphabet         = "abcdefghijklmnopqrstuvwxyz234567"
	generatedNameLen = 12 // 60 bits of entropy
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)

// reserved names would collide with HTTP routes (/api, /healthz).
var reserved = map[string]bool{"api": true, "healthz": true}

// ValidName reports whether name can be used as the local part of an inbox.
func ValidName(name string) bool {
	return nameRe.MatchString(name) && !reserved[name]
}

// Config bounds the store. All limits are hard limits.
type Config struct {
	Domain          string
	TTL             time.Duration
	MaxInboxes      int // live inboxes; creating more fails with ErrCapacity
	MaxMsgsPerInbox int // beyond this the inbox's oldest message rotates out
	MaxStoreBytes   int // beyond this the globally oldest message rotates out
}

// Message is an immutable received e-mail. Fields below the blank line are
// store-internal bookkeeping.
type Message struct {
	Seq      int
	From     string
	Subject  string
	Received time.Time
	NAttach  int
	Raw      []byte

	inbox *Inbox
	elem  *list.Element
}

type Inbox struct {
	Address string
	Expires time.Time
	msgs    []*Message
	seq     int
	bytes   int
	ch      chan struct{} // closed and replaced whenever a message arrives
}

type MsgMeta struct {
	ID          string    `json:"id"`
	From        string    `json:"from"`
	Subject     string    `json:"subject"`
	ReceivedAt  time.Time `json:"received_at"`
	Size        int       `json:"size"`
	Attachments int       `json:"attachments"`
}

type View struct {
	Address   string    `json:"address"`
	ExpiresAt time.Time `json:"expires_at"`
	ExpiresIn int       `json:"expires_in"`
	Messages  []MsgMeta `json:"messages"`
}

type Stats struct {
	Inboxes  int   `json:"inboxes"`
	Messages int   `json:"messages"`
	Bytes    int   `json:"bytes"`
	MaxBytes int   `json:"max_bytes"`
	Evicted  int64 `json:"evicted_messages"`
}

type Store struct {
	mu      sync.Mutex
	cfg     Config
	now     func() time.Time
	inboxes map[string]*Inbox
	total   int
	order   *list.List // every message, oldest first (global FIFO for rotation)
	evicted int64
}

func New(cfg Config) *Store { return NewWithClock(cfg, time.Now) }

// NewWithClock lets tests control time.
func NewWithClock(cfg Config, now func() time.Time) *Store {
	return &Store{cfg: cfg, now: now, inboxes: map[string]*Inbox{}, order: list.New()}
}

func randName(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("inbox: crypto/rand unavailable: " + err.Error())
	}
	for i := range b {
		b[i] = alphabet[b[i]&31]
	}
	return string(b)
}

// Create makes a new inbox. An empty name generates a random one.
func (s *Store) Create(name string) (View, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name != "" && !ValidName(name) {
		return View{}, ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()
	if len(s.inboxes) >= s.cfg.MaxInboxes {
		return View{}, ErrCapacity
	}
	if name == "" {
		for {
			name = randName(generatedNameLen)
			if _, used := s.inboxes[name+"@"+s.cfg.Domain]; !used {
				break
			}
		}
	}
	addr := name + "@" + s.cfg.Domain
	if _, used := s.inboxes[addr]; used {
		return View{}, ErrTaken
	}
	ib := &Inbox{Address: addr, Expires: s.now().Add(s.cfg.TTL), ch: make(chan struct{})}
	s.inboxes[addr] = ib
	return s.viewLocked(ib, 0), nil
}

// live returns the inbox if it exists and has not expired. As a side effect
// it drops an expired inbox, so expiry never depends on the janitor's timing.
//
// addr may be a bare name ("my-box"): the configured domain is implied.
func (s *Store) live(addr string) *Inbox {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if !strings.Contains(addr, "@") {
		addr += "@" + s.cfg.Domain
	}
	ib := s.inboxes[addr]
	if ib == nil {
		return nil
	}
	if !s.now().Before(ib.Expires) {
		s.dropLocked(ib)
		return nil
	}
	return ib
}

func (s *Store) evictLocked(m *Message) {
	ib := m.inbox
	for i, x := range ib.msgs {
		if x == m {
			ib.msgs = append(ib.msgs[:i], ib.msgs[i+1:]...)
			break
		}
	}
	ib.bytes -= len(m.Raw)
	s.total -= len(m.Raw)
	s.order.Remove(m.elem)
	s.evicted++
}

func (s *Store) dropLocked(ib *Inbox) {
	for _, m := range ib.msgs {
		s.order.Remove(m.elem)
	}
	s.total -= ib.bytes
	delete(s.inboxes, ib.Address)
	close(ib.ch) // wakes long-pollers so they observe the expiry
}

func (s *Store) Exists(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live(addr) != nil
}

func (s *Store) Delete(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ib := s.live(addr)
	if ib == nil {
		return false
	}
	s.dropLocked(ib)
	return true
}

// Deliver stores m in the inbox, rotating out the oldest messages if needed.
// The newest mail is the one a user is waiting for, so we never refuse it
// because the store is full.
func (s *Store) Deliver(addr string, m *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ib := s.live(addr)
	if ib == nil {
		return ErrNoInbox
	}
	if len(m.Raw) > s.cfg.MaxStoreBytes {
		return ErrTooLarge
	}
	for len(ib.msgs) >= s.cfg.MaxMsgsPerInbox {
		s.evictLocked(ib.msgs[0])
	}
	for s.total+len(m.Raw) > s.cfg.MaxStoreBytes {
		front := s.order.Front()
		if front == nil {
			break
		}
		s.evictLocked(front.Value.(*Message))
	}
	ib.seq++
	m.Seq = ib.seq
	m.inbox = ib
	m.elem = s.order.PushBack(m)
	ib.msgs = append(ib.msgs, m)
	ib.bytes += len(m.Raw)
	s.total += len(m.Raw)
	close(ib.ch)
	ib.ch = make(chan struct{})
	return nil
}

func (s *Store) viewLocked(ib *Inbox, after int) View {
	v := View{
		Address:   ib.Address,
		ExpiresAt: ib.Expires,
		ExpiresIn: int(ib.Expires.Sub(s.now()).Seconds()),
		Messages:  []MsgMeta{},
	}
	for _, m := range ib.msgs {
		if m.Seq > after {
			v.Messages = append(v.Messages, MsgMeta{
				ID: strconv.Itoa(m.Seq), From: m.From, Subject: m.Subject,
				ReceivedAt: m.Received, Size: len(m.Raw), Attachments: m.NAttach,
			})
		}
	}
	return v
}

// List returns messages with sequence > after. If wait > 0 and nothing is
// new it long-polls until a message arrives, the inbox expires, the wait
// elapses or ctx ends. The bool is false when the inbox does not exist.
func (s *Store) List(ctx context.Context, addr string, after int, wait time.Duration) (View, bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		s.mu.Lock()
		ib := s.live(addr)
		if ib == nil {
			s.mu.Unlock()
			return View{}, false
		}
		v := s.viewLocked(ib, after)
		ch := ib.ch
		untilExpiry := ib.Expires.Sub(s.now())
		s.mu.Unlock()
		if len(v.Messages) > 0 || wait <= 0 {
			return v, true
		}
		expiry := time.NewTimer(untilExpiry)
		select {
		case <-ch:
		case <-timer.C:
			wait = 0
		case <-expiry.C:
		case <-ctx.Done():
			wait = 0
		}
		expiry.Stop()
	}
}

func (s *Store) Get(addr, id string) (*Message, bool) {
	seq, err := strconv.Atoi(id)
	if err != nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ib := s.live(addr)
	if ib == nil {
		return nil, false
	}
	for _, m := range ib.msgs {
		if m.Seq == seq {
			return m, true
		}
	}
	return nil, false
}

func (s *Store) reapLocked() {
	now := s.now()
	for _, ib := range s.inboxes {
		if !now.Before(ib.Expires) {
			s.dropLocked(ib)
		}
	}
}

// Reap drops expired inboxes. Call it periodically to reclaim memory from
// inboxes nobody touches again.
func (s *Store) Reap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()
}

func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{len(s.inboxes), s.order.Len(), s.total, s.cfg.MaxStoreBytes, s.evicted}
}
