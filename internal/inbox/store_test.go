package inbox

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTest(cfg Config) (*Store, *fakeClock) {
	c := &fakeClock{t: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	if cfg.Domain == "" {
		cfg.Domain = "mail.test"
	}
	if cfg.TTL == 0 {
		cfg.TTL = 5 * time.Minute
	}
	if cfg.MaxInboxes == 0 {
		cfg.MaxInboxes = 100
	}
	if cfg.MaxMsgsPerInbox == 0 {
		cfg.MaxMsgsPerInbox = 30
	}
	if cfg.MaxStoreBytes == 0 {
		cfg.MaxStoreBytes = 1 << 20
	}
	return NewWithClock(cfg, c.Now), c
}

func ids(v View) (out []string) {
	for _, m := range v.Messages {
		out = append(out, m.ID)
	}
	return
}

func msg(n int) *Message { return &Message{Raw: make([]byte, n), Subject: "s"} }

func TestInboxExpiresAtTTL(t *testing.T) {
	st, clk := newTest(Config{})
	v, err := st.Create("")
	if err != nil {
		t.Fatal(err)
	}
	if v.ExpiresIn != 300 {
		t.Fatalf("expires_in = %d, want 300", v.ExpiresIn)
	}
	clk.Advance(5*time.Minute - time.Second)
	if !st.Exists(v.Address) {
		t.Fatal("must still exist 1s before TTL")
	}
	clk.Advance(time.Second)
	if st.Exists(v.Address) {
		t.Fatal("must be gone at TTL")
	}
	if err := st.Deliver(v.Address, msg(1)); err != ErrNoInbox {
		t.Fatalf("deliver to expired inbox: got %v", err)
	}
}

func TestTTLIsNotRenewedByMail(t *testing.T) {
	st, clk := newTest(Config{})
	v, _ := st.Create("")
	clk.Advance(4 * time.Minute)
	st.Deliver(v.Address, msg(1))
	clk.Advance(time.Minute)
	if st.Exists(v.Address) {
		t.Fatal("receiving mail must not extend the TTL")
	}
}

func TestNames(t *testing.T) {
	st, _ := newTest(Config{})
	for _, bad := range []string{"ab", "-abc", "a b c", "UPPER_ok_but_>32_chars_long_xxxxxxxxxxxxxxxx", "a@b.c", "../x"} {
		if _, err := st.Create(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("%q: want ErrBadName, got %v", bad, err)
		}
	}
	if _, err := st.Create("Hello"); err != nil {
		t.Fatal(err) // case-insensitive
	}
	if _, err := st.Create("hello"); err != ErrTaken {
		t.Fatalf("want ErrTaken, got %v", err)
	}
	a, _ := st.Create("")
	b, _ := st.Create("")
	if a.Address == b.Address {
		t.Fatal("generated names must differ")
	}
}

func TestBareNameResolvesToConfiguredDomain(t *testing.T) {
	st, _ := newTest(Config{})
	v, _ := st.Create("my-box")
	st.Deliver("my-box", msg(3)) // bare
	got, ok := st.List(context.Background(), "MY-BOX", 0, 0)
	if !ok || got.Address != v.Address || len(got.Messages) != 1 {
		t.Fatalf("bare name lookup failed: ok=%v %+v", ok, got)
	}
	if st.Exists("my-box@other.test") {
		t.Fatal("another domain must not resolve")
	}
}

func TestCheckName(t *testing.T) {
	cases := []struct {
		name string
		want error
	}{
		{"", ErrNameTooShort}, {"a", ErrNameTooShort}, {"ab", ErrNameTooShort},
		{"abc", nil}, {"automacao202511012205222", nil}, {"a.b_c-d", nil},
		{strings.Repeat("a", 32), nil}, {strings.Repeat("a", 33), ErrNameTooLong},
		{"api", ErrNameReserved}, {"healthz", ErrNameReserved},
		{"-abc", ErrNameChars}, {".abc", ErrNameChars}, {"a b", ErrNameChars}, {"a@b", ErrNameChars},
		{"ABC", ErrNameChars}, // callers lower-case first
	}
	for _, c := range cases {
		if got := CheckName(c.name); got != c.want {
			t.Errorf("CheckName(%q) = %v, want %v", c.name, got, c.want)
		}
		if c.want != nil && !errors.Is(c.want, ErrBadName) {
			t.Errorf("%v must wrap ErrBadName", c.want)
		}
	}
}

func TestReservedNames(t *testing.T) {
	st, _ := newTest(Config{})
	for _, n := range []string{"api", "healthz"} {
		if _, err := st.Create(n); !errors.Is(err, ErrBadName) {
			t.Errorf("%q must be reserved, got %v", n, err)
		}
	}
}

func TestCapacity(t *testing.T) {
	st, clk := newTest(Config{MaxInboxes: 2})
	st.Create("")
	st.Create("")
	if _, err := st.Create(""); err != ErrCapacity {
		t.Fatalf("want ErrCapacity, got %v", err)
	}
	clk.Advance(6 * time.Minute) // expired inboxes free their slots
	if _, err := st.Create(""); err != nil {
		t.Fatalf("slots must be reclaimed: %v", err)
	}
}

func TestRotationPerInbox(t *testing.T) {
	st, _ := newTest(Config{MaxMsgsPerInbox: 2})
	v, _ := st.Create("")
	for i := 0; i < 3; i++ {
		if err := st.Deliver(v.Address, msg(1)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.List(context.Background(), v.Address, 0, 0)
	if g := ids(got); len(g) != 2 || g[0] != "2" || g[1] != "3" {
		t.Fatalf("oldest must rotate out, got %v", g)
	}
}

func TestRotationGlobalBytes(t *testing.T) {
	st, _ := newTest(Config{MaxStoreBytes: 100})
	a, _ := st.Create("")
	b, _ := st.Create("")
	st.Deliver(a.Address, msg(40)) // oldest
	st.Deliver(b.Address, msg(40))
	st.Deliver(a.Address, msg(40)) // 120 > 100 -> evicts a#1
	va, _ := st.List(context.Background(), a.Address, 0, 0)
	vb, _ := st.List(context.Background(), b.Address, 0, 0)
	if g := ids(va); len(g) != 1 || g[0] != "2" {
		t.Fatalf("inbox a: %v", g)
	}
	if len(vb.Messages) != 1 {
		t.Fatal("inbox b must be untouched")
	}
	if s := st.Stats(); s.Bytes != 80 || s.Messages != 2 || s.Evicted != 1 {
		t.Fatalf("stats: %+v", s)
	}
	if err := st.Deliver(a.Address, msg(101)); err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestDeleteAndExpiryFreeAccounting(t *testing.T) {
	st, clk := newTest(Config{})
	a, _ := st.Create("")
	b, _ := st.Create("")
	st.Deliver(a.Address, msg(10))
	st.Deliver(b.Address, msg(20))
	st.Delete(a.Address)
	if s := st.Stats(); s.Bytes != 20 || s.Messages != 1 || s.Inboxes != 1 {
		t.Fatalf("after delete: %+v", s)
	}
	clk.Advance(10 * time.Minute)
	st.Reap()
	if s := st.Stats(); s.Bytes != 0 || s.Messages != 0 || s.Inboxes != 0 {
		t.Fatalf("after reap: %+v", s)
	}
}

func TestLongPollWakesOnMessage(t *testing.T) {
	st, _ := newTest(Config{})
	v, _ := st.Create("")
	go func() {
		time.Sleep(50 * time.Millisecond)
		st.Deliver(v.Address, &Message{Subject: "hi", Raw: []byte("x")})
	}()
	got, ok := st.List(context.Background(), v.Address, 0, 5*time.Second)
	if !ok || len(got.Messages) != 1 || got.Messages[0].Subject != "hi" {
		t.Fatalf("long-poll did not return the message: %+v", got)
	}
}

func TestLongPollHonoursContext(t *testing.T) {
	st, _ := newTest(Config{})
	v, _ := st.Create("")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	got, ok := st.List(ctx, v.Address, 0, 30*time.Second)
	if !ok || len(got.Messages) != 0 || time.Since(start) > 2*time.Second {
		t.Fatal("must return promptly with no messages when ctx ends")
	}
}

func TestGet(t *testing.T) {
	st, _ := newTest(Config{})
	v, _ := st.Create("")
	st.Deliver(v.Address, msg(5))
	if _, ok := st.Get(v.Address, "1"); !ok {
		t.Fatal("message 1 must exist")
	}
	for _, id := range []string{"0", "2", "x", "-1", ""} {
		if _, ok := st.Get(v.Address, id); ok {
			t.Errorf("id %q must not resolve", id)
		}
	}
}

func TestConcurrentUse(t *testing.T) { // run with -race
	st, _ := newTest(Config{MaxMsgsPerInbox: 5, MaxStoreBytes: 200})
	v, _ := st.Create("")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				st.Deliver(v.Address, msg(30))
				st.List(context.Background(), v.Address, 0, 0)
				st.Stats()
			}
		}()
	}
	wg.Wait()
	if s := st.Stats(); s.Bytes > 200 {
		t.Fatalf("cap exceeded: %+v", s)
	}
}
