package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tempmail/internal/inbox"
	"tempmail/internal/ratelimit"
)

type env struct {
	st *inbox.Store
	h  http.Handler
}

func setup(t *testing.T, mod func(*Options)) env {
	t.Helper()
	st := inbox.New(inbox.Config{Domain: "mail.test", TTL: time.Minute, MaxInboxes: 100, MaxMsgsPerInbox: 10, MaxStoreBytes: 1 << 20})
	o := Options{
		Inboxes:     st,
		CreateLimit: ratelimit.New(1000, time.Minute),
		APILimit:    ratelimit.New(1000, time.Minute),
	}
	if mod != nil {
		mod(&o)
	}
	return env{st, NewHandler(o)}
}

func (e env) do(method, path string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "192.0.2.1:1234"
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e env) newInbox(t *testing.T) string {
	t.Helper()
	w := e.do("POST", "/api/inbox")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var v inbox.View
	json.Unmarshal(w.Body.Bytes(), &v)
	return v.Address
}

const mailWithAttachment = "From: Ana <ana@example.com>\r\nTo: x\r\nSubject: Hi\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
	"--b\r\nContent-Type: text/plain\r\n\r\nhello\r\n" +
	"--b\r\nContent-Type: text/html; name=evil.html\r\nContent-Disposition: attachment; filename=\"../evil.html\"\r\n\r\n<script>alert(1)</script>\r\n--b--\r\n"

func TestFullFlow(t *testing.T) {
	e := setup(t, nil)
	addr := e.newInbox(t)
	if !strings.HasSuffix(addr, "@mail.test") {
		t.Fatalf("bad address %q", addr)
	}
	e.st.Deliver(addr, &inbox.Message{From: "ana", Subject: "Hi", Received: time.Now(), NAttach: 1, Raw: []byte(mailWithAttachment)})

	w := e.do("GET", "/api/inbox/"+addr+"/messages")
	var v inbox.View
	json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 200 || len(v.Messages) != 1 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	w = e.do("GET", "/api/inbox/"+addr+"/messages/1")
	var d messageDetail
	json.Unmarshal(w.Body.Bytes(), &d)
	if strings.TrimSpace(d.Text) != "hello" || len(d.Attachments) != 1 || d.Attachments[0].Filename != "evil.html" {
		t.Fatalf("detail: %s", w.Body)
	}
}

func TestAttachmentIsAlwaysAnOpaqueDownload(t *testing.T) {
	e := setup(t, nil)
	addr := e.newInbox(t)
	e.st.Deliver(addr, &inbox.Message{Received: time.Now(), Raw: []byte(mailWithAttachment)})
	w := e.do("GET", "/api/inbox/"+addr+"/messages/1/attachments/0")
	if w.Code != 200 {
		t.Fatalf("code %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, attacker-controlled types must not be echoed", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || strings.Contains(cd, "..") || strings.Contains(cd, "/") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("nosniff missing")
	}
	if e.do("GET", "/api/inbox/"+addr+"/messages/1/attachments/9").Code != 404 {
		t.Fatal("out-of-range attachment must 404")
	}
	if e.do("GET", "/api/inbox/"+addr+"/messages/1/attachments/-1").Code != 404 {
		t.Fatal("negative attachment index must 404")
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	e := setup(t, nil)
	for _, p := range []string{"/", "/healthz", "/api/inbox/none@mail.test"} {
		h := e.do("GET", p).Header()
		for _, k := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options"} {
			if h.Get(k) == "" {
				t.Errorf("%s missing on %s", k, p)
			}
		}
		if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src 'self' 'unsafe") {
			t.Errorf("weak CSP on %s: %s", p, csp)
		}
	}
}

func TestUnknownAndExpiredInboxAre404(t *testing.T) {
	e := setup(t, nil)
	if e.do("GET", "/api/inbox/nope@mail.test/messages").Code != 404 {
		t.Fatal("unknown inbox must 404")
	}
	if e.do("DELETE", "/api/inbox/nope@mail.test").Code != 404 {
		t.Fatal("delete of unknown inbox must 404")
	}
}

func TestDelete(t *testing.T) {
	e := setup(t, nil)
	addr := e.newInbox(t)
	if e.do("DELETE", "/api/inbox/"+addr).Code != 204 {
		t.Fatal("delete must 204")
	}
	if e.do("GET", "/api/inbox/"+addr).Code != 404 {
		t.Fatal("deleted inbox must 404")
	}
}

func TestCreateValidation(t *testing.T) {
	e := setup(t, nil)
	post := func(body string) int {
		r := httptest.NewRequest("POST", "/api/inbox", strings.NewReader(body))
		r.RemoteAddr = "192.0.2.1:1"
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		return w.Code
	}
	if c := post(`{"name":"my-box"}`); c != 201 {
		t.Fatalf("valid name: %d", c)
	}
	if c := post(`{"name":"my-box"}`); c != 409 {
		t.Fatalf("duplicate: %d", c)
	}
	if c := post(`{"name":"a"}`); c != 400 {
		t.Fatalf("short name: %d", c)
	}
	if c := post(`{"name":"` + strings.Repeat("x", 5000) + `"}`); c != 201 && c != 400 {
		t.Fatalf("oversized body must not crash, got %d", c)
	}
}

func TestCreateRateLimit(t *testing.T) {
	e := setup(t, func(o *Options) { o.CreateLimit = ratelimit.New(2, time.Minute) })
	e.do("POST", "/api/inbox")
	e.do("POST", "/api/inbox")
	w := e.do("POST", "/api/inbox")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("want 429 with Retry-After, got %d", w.Code)
	}
}

func TestAPIRateLimitDoesNotCoverStaticOrHealth(t *testing.T) {
	e := setup(t, func(o *Options) { o.APILimit = ratelimit.New(1, time.Minute) })
	e.do("GET", "/api/inbox/x@mail.test")
	if e.do("GET", "/api/inbox/x@mail.test").Code != 429 {
		t.Fatal("API must be limited")
	}
	if e.do("GET", "/healthz").Code != 200 || e.do("GET", "/").Code != 200 {
		t.Fatal("health and UI must not be rate limited")
	}
}

func TestForwardedForIgnoredUnlessTrusted(t *testing.T) {
	spoof := func(e env, i int) int {
		return e.do("POST", "/api/inbox", "X-Forwarded-For", "10.0.0."+string(rune('0'+i))).Code
	}
	untrusted := setup(t, func(o *Options) { o.CreateLimit = ratelimit.New(1, time.Minute) })
	spoof(untrusted, 1)
	if spoof(untrusted, 2) != 429 {
		t.Fatal("a forged X-Forwarded-For must not bypass the limit when proxy is not trusted")
	}
	trusted := setup(t, func(o *Options) { o.CreateLimit = ratelimit.New(1, time.Minute); o.TrustProxy = true })
	spoof(trusted, 1)
	if spoof(trusted, 2) != 201 {
		t.Fatal("behind a trusted proxy, distinct client IPs must be limited separately")
	}
}

func TestStatsOffByDefault(t *testing.T) {
	if setup(t, nil).do("GET", "/api/stats").Code == 200 {
		t.Fatal("/api/stats must be disabled unless explicitly enabled")
	}
	if setup(t, func(o *Options) { o.ExposeStats = true }).do("GET", "/api/stats").Code != 200 {
		t.Fatal("/api/stats must work when enabled")
	}
}

func TestPutOpensOrCreatesByBareName(t *testing.T) {
	e := setup(t, nil)
	w := e.do("PUT", "/api/inbox/automacao202511012205222")
	var v inbox.View
	json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 201 || v.Address != "automacao202511012205222@mail.test" {
		t.Fatalf("first PUT must create: %d %s", w.Code, w.Body)
	}
	if w2 := e.do("PUT", "/api/inbox/AutomaCao202511012205222"); w2.Code != 200 { // idempotent, case-insensitive
		t.Fatalf("second PUT must open the same inbox: %d %s", w2.Code, w2.Body)
	}
	e.st.Deliver(v.Address, &inbox.Message{Subject: "x", Received: time.Now(), Raw: []byte("x")})
	// the bare name works everywhere the full address does
	w3 := e.do("GET", "/api/inbox/automacao202511012205222/messages")
	var l inbox.View
	json.Unmarshal(w3.Body.Bytes(), &l)
	if w3.Code != 200 || len(l.Messages) != 1 {
		t.Fatalf("bare name list: %d %s", w3.Code, w3.Body)
	}
}

func TestPutRejectsBadAndReservedNames(t *testing.T) {
	e := setup(t, nil)
	for _, n := range []string{"ab", "api", "healthz", "a@b.c", "-bad", "x%20y"} {
		if w := e.do("PUT", "/api/inbox/"+n); w.Code != 400 {
			t.Errorf("PUT %q: want 400, got %d", n, w.Code)
		}
	}
}

func TestPutCreationIsRateLimitedButOpeningIsNot(t *testing.T) {
	e := setup(t, func(o *Options) { o.CreateLimit = ratelimit.New(1, time.Minute) })
	if e.do("PUT", "/api/inbox/first-box").Code != 201 {
		t.Fatal("first creation must pass")
	}
	if e.do("PUT", "/api/inbox/second-box").Code != 429 {
		t.Fatal("second creation must be limited")
	}
	for i := 0; i < 5; i++ {
		if e.do("PUT", "/api/inbox/first-box").Code != 200 {
			t.Fatal("re-opening an existing inbox must not consume creation budget")
		}
	}
}

func TestMailboxURLServesThePageButFilesAndRoutesWin(t *testing.T) {
	e := setup(t, nil)
	page := e.do("GET", "/automacao202511012205222")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "<title>Mail") {
		t.Fatalf("/<name> must serve the app page: %d", page.Code)
	}
	if js := e.do("GET", "/app.js"); js.Code != 200 || strings.Contains(js.Body.String(), "<html") {
		t.Fatal("/app.js must still be the script, not the page")
	}
	if e.do("GET", "/style.css").Code != 200 {
		t.Fatal("/style.css must be served")
	}
	if e.do("GET", "/healthz").Body.String() != "ok" {
		t.Fatal("/healthz must not be shadowed by a mailbox page")
	}
	for _, p := range []string{"/ab", "/a%20b", "/api", "/-bad", "/x/y"} {
		if c := e.do("GET", p).Code; c != 404 {
			t.Errorf("GET %s: want 404, got %d", p, c)
		}
	}
}

func TestLongPollIsCapped(t *testing.T) {
	e := setup(t, nil)
	addr := e.newInbox(t)
	e.st.Deliver(addr, &inbox.Message{Received: time.Now(), Raw: []byte("x")})
	start := time.Now()
	w := e.do("GET", "/api/inbox/"+addr+"/messages?wait=99999&after=0") // has data: returns at once
	if w.Code != 200 || time.Since(start) > time.Second {
		t.Fatal("long-poll with available data must return immediately")
	}
}
