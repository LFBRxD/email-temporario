package smtpd

import (
	"bufio"
	"context"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"tempmail/internal/inbox"
)

func start(t *testing.T, o Options) (*inbox.Store, string) {
	t.Helper()
	st := inbox.New(inbox.Config{Domain: "mail.test", TTL: time.Minute, MaxInboxes: 10, MaxMsgsPerInbox: 10, MaxStoreBytes: 1 << 20})
	o.Domain, o.MaxMsgBytes = "mail.test", 64*1024
	if o.MaxConns == 0 {
		o.MaxConns = 50
	}
	if o.MaxConnsPerIP == 0 {
		o.MaxConnsPerIP = 50
	}
	if o.MaxConcurrentData == 0 {
		o.MaxConcurrentData = 4
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, o)
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return st, l.Addr().String()
}

func send(addr, to, body string) error {
	return smtp.SendMail(addr, nil, "sender@example.com", []string{to}, []byte(body))
}

const mailBody = "From: Ana <ana@example.com>\r\nTo: x\r\nSubject: Hello\r\n\r\nbody\r\n"

func TestDeliversToLiveInbox(t *testing.T) {
	st, addr := start(t, Options{})
	v, _ := st.Create("")
	if err := send(addr, v.Address, mailBody); err != nil {
		t.Fatal(err)
	}
	got, _ := st.List(context.Background(), v.Address, 0, 0)
	if len(got.Messages) != 1 || got.Messages[0].Subject != "Hello" {
		t.Fatalf("not delivered: %+v", got)
	}
}

func TestRejectsUnknownRecipientWith550(t *testing.T) {
	_, addr := start(t, Options{})
	err := send(addr, "nobody@mail.test", mailBody)
	if err == nil || !strings.HasPrefix(err.Error(), "550") {
		t.Fatalf("want 550, got %v", err)
	}
}

func TestRejectsOtherDomains(t *testing.T) { // never an open relay
	_, addr := start(t, Options{})
	err := send(addr, "someone@example.org", mailBody)
	if err == nil || !strings.HasPrefix(err.Error(), "550") {
		t.Fatalf("want 550, got %v", err)
	}
}

type fixedLimit struct{ n int }

func (f *fixedLimit) Allow(string) bool { f.n--; return f.n >= 0 }

func TestAutoCreateOffByDefault(t *testing.T) {
	st, addr := start(t, Options{})
	err := send(addr, "automacao1@mail.test", mailBody)
	if err == nil || !strings.HasPrefix(err.Error(), "550") || st.Exists("automacao1@mail.test") {
		t.Fatalf("auto-create must be off by default, got %v", err)
	}
}

func TestAutoCreateOnMail(t *testing.T) {
	st, addr := start(t, Options{AutoCreate: &fixedLimit{n: 100}})
	if err := send(addr, "automacao202511012205222@mail.test", mailBody); err != nil {
		t.Fatal(err)
	}
	got, ok := st.List(context.Background(), "automacao202511012205222", 0, 0) // bare name works too
	if !ok || len(got.Messages) != 1 {
		t.Fatalf("mail must create the inbox and be stored: ok=%v %+v", ok, got)
	}
}

func TestAutoCreateStillRefusesForeignDomainsAndBadNames(t *testing.T) {
	st, addr := start(t, Options{AutoCreate: &fixedLimit{n: 100}})
	for _, to := range []string{"x@example.org", "ab@mail.test", "api@mail.test", "-bad@mail.test"} {
		err := send(addr, to, mailBody)
		if err == nil || !strings.HasPrefix(err.Error(), "550") {
			t.Errorf("%q: want 550, got %v", to, err)
		}
	}
	if s := st.Stats(); s.Inboxes != 0 {
		t.Fatalf("nothing must be created, got %d inboxes", s.Inboxes)
	}
}

func TestAutoCreateIsRateLimited(t *testing.T) {
	_, addr := start(t, Options{AutoCreate: &fixedLimit{n: 1}})
	if err := send(addr, "first-box@mail.test", mailBody); err != nil {
		t.Fatal(err)
	}
	err := send(addr, "second-box@mail.test", mailBody)
	if err == nil || !strings.HasPrefix(err.Error(), "450") {
		t.Fatalf("want temporary 450 once limited, got %v", err)
	}
}

func TestRejectsOversizedMessage(t *testing.T) {
	st, addr := start(t, Options{})
	v, _ := st.Create("")
	err := send(addr, v.Address, mailBody+strings.Repeat("A", 100*1024)+"\r\n")
	if err == nil {
		t.Fatal("message above MaxMsgBytes must be refused")
	}
	if got, _ := st.List(context.Background(), v.Address, 0, 0); len(got.Messages) != 0 {
		t.Fatal("oversized message must not be stored")
	}
}

func TestPerIPConnectionLimit(t *testing.T) {
	_, addr := start(t, Options{MaxConnsPerIP: 2})
	var open []net.Conn
	defer func() {
		for _, c := range open {
			c.Close()
		}
	}()
	var banners []string
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, c)
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, _ := bufio.NewReader(c).ReadString('\n')
		banners = append(banners, line)
	}
	if !strings.HasPrefix(banners[0], "220") || !strings.HasPrefix(banners[1], "220") {
		t.Fatalf("first two must be served: %q", banners)
	}
	if !strings.HasPrefix(banners[2], "421") {
		t.Fatalf("third must be refused with 421, got %q", banners[2])
	}
}

func TestSlotIsFreedWhenConnectionCloses(t *testing.T) {
	_, addr := start(t, Options{MaxConnsPerIP: 1})
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, _ := bufio.NewReader(c).ReadString('\n')
		if !strings.HasPrefix(line, "220") {
			t.Fatalf("iteration %d: slot not released, got %q", i, line)
		}
		c.Close()
		time.Sleep(50 * time.Millisecond)
	}
}
