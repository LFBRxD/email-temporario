// Package httpapi serves the JSON API and the embedded web UI.
package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tempmail/internal/inbox"
	"tempmail/internal/mailparse"
)

//go:embed web
var webFS embed.FS

// Inboxes is what the API needs from the mailbox store.
type Inboxes interface {
	Create(name string) (inbox.View, error)
	List(ctx context.Context, addr string, after int, wait time.Duration) (inbox.View, bool)
	Get(addr, id string) (*inbox.Message, bool)
	Delete(addr string) bool
	Stats() inbox.Stats
}

// Limiter decides whether key may perform one more action right now.
type Limiter interface{ Allow(key string) bool }

type Options struct {
	Inboxes     Inboxes
	CreateLimit Limiter // inbox creations, per client IP
	APILimit    Limiter // all /api requests, per client IP
	TrustProxy  bool    // honour X-Forwarded-For / X-Real-IP
	ExposeStats bool    // serve /api/stats
}

type api struct{ Options }

const maxLongPoll = 30 * time.Second

func NewHandler(o Options) http.Handler {
	a := &api{o}
	mux := http.NewServeMux()
	route := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, a.limited(h)) }

	route("POST /api/inbox", a.create)
	route("PUT /api/inbox/{addr}", a.open)
	route("GET /api/inbox/{addr}", a.list)
	route("DELETE /api/inbox/{addr}", a.remove)
	route("GET /api/inbox/{addr}/messages", a.list)
	route("GET /api/inbox/{addr}/messages/{id}", a.message)
	route("GET /api/inbox/{addr}/messages/{id}/raw", a.raw)
	route("GET /api/inbox/{addr}/messages/{id}/attachments/{aid}", a.attachment)
	if o.ExposeStats {
		route("GET /api/stats", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, o.Inboxes.Stats()) })
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	sub, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(sub))
	mux.Handle("GET /", files)
	// "/<name>" is a mailbox URL: the page reads the name from the path and
	// opens (creating on demand) that inbox. Real static files win over names.
	mux.HandleFunc("GET /{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if f, err := sub.Open(name); err == nil {
			_ = f.Close()
			files.ServeHTTP(w, r)
			return
		}
		if err := inbox.CheckName(strings.ToLower(name)); err != nil {
			// Bad name: back to the welcome page, which explains why.
			http.Redirect(w, r, "/?erro="+nameErrorCode(err), http.StatusFound)
			return
		}
		http.ServeFileFS(w, r, sub, "index.html")
	})
	// "/name/" (trailing slash) is the same mailbox; deeper paths are unknown.
	mux.HandleFunc("GET /{name}/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+r.PathValue("name")+"/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/"+r.PathValue("name"), http.StatusFound)
	})
	return securityHeaders(mux)
}

// nameErrorCode is the short code the welcome page turns into a message.
// Codes (not free text) travel in the URL so the page never renders
// attacker-supplied text.
func nameErrorCode(err error) string {
	switch {
	case errors.Is(err, inbox.ErrNameTooShort):
		return "curto"
	case errors.Is(err, inbox.ErrNameTooLong):
		return "longo"
	case errors.Is(err, inbox.ErrNameReserved):
		return "reservado"
	default:
		return "invalido"
	}
}

func (a *api) limited(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.APILimit.Allow(clientIP(r, a.TrustProxy)) {
			w.Header().Set("Retry-After", "30")
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next(w, r)
	})
}

// clientIP returns the caller's IP. Forwarding headers are honoured only when
// explicitly trusted: otherwise any client could forge them and dodge limits.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.Split(xf, ",")[0])
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return strings.TrimSpace(xr)
		}
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		// Mail HTML is rendered in a sandboxed srcdoc iframe, which inherits
		// this policy: no scripts, no remote images/fonts/frames/forms.
		h.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
				"connect-src 'self'; frame-src about:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *api) create(w http.ResponseWriter, r *http.Request) {
	if !a.CreateLimit.Allow(clientIP(r, a.TrustProxy)) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "too many inboxes created, slow down")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body)
	}
	v, err := a.Inboxes.Create(body.Name)
	if err != nil {
		writeCreateErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func writeCreateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, inbox.ErrBadName):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, inbox.ErrTaken):
		writeErr(w, http.StatusConflict, "address already in use")
	case errors.Is(err, inbox.ErrCapacity):
		writeErr(w, http.StatusServiceUnavailable, "service at capacity, try again later")
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

// open is idempotent: it returns the inbox called {addr} (200) or creates it
// (201). This is what makes a URL like /my-box usable as a mailbox.
func (a *api) open(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(r.PathValue("addr"))
	if v, ok := a.Inboxes.List(r.Context(), name, 0, 0); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}
	if !a.CreateLimit.Allow(clientIP(r, a.TrustProxy)) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "too many inboxes created, slow down")
		return
	}
	v, err := a.Inboxes.Create(name)
	if errors.Is(err, inbox.ErrTaken) { // created concurrently
		if v, ok := a.Inboxes.List(r.Context(), name, 0, 0); ok {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	if err != nil {
		writeCreateErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// list supports ?after=<id> and ?wait=<seconds, max 30> (long-poll).
func (a *api) list(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	secs, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	wait := time.Duration(secs) * time.Second
	if wait < 0 {
		wait = 0
	}
	if wait > maxLongPoll {
		wait = maxLongPoll
	}
	v, ok := a.Inboxes.List(r.Context(), r.PathValue("addr"), after, wait)
	if !ok {
		writeErr(w, http.StatusNotFound, "inbox not found or expired")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *api) remove(w http.ResponseWriter, r *http.Request) {
	if !a.Inboxes.Delete(r.PathValue("addr")) {
		writeErr(w, http.StatusNotFound, "inbox not found or expired")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type attachmentInfo struct {
	ID          int    `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
}

type messageDetail struct {
	inbox.MsgMeta
	To          string           `json:"to"`
	Text        string           `json:"text"`
	HTML        string           `json:"html"`
	Attachments []attachmentInfo `json:"attachments"`
}

func (a *api) message(w http.ResponseWriter, r *http.Request) {
	m, ok := a.Inboxes.Get(r.PathValue("addr"), r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	p := mailparse.Parse(m.Raw)
	d := messageDetail{
		MsgMeta: inbox.MsgMeta{
			ID: r.PathValue("id"), From: p.From, Subject: p.Subject,
			ReceivedAt: m.Received, Size: len(m.Raw), Attachments: len(p.Attachments),
		},
		To: p.To, Text: p.Text, HTML: p.HTML, Attachments: []attachmentInfo{},
	}
	for i, at := range p.Attachments {
		d.Attachments = append(d.Attachments, attachmentInfo{i, at.Filename, at.ContentType, len(at.Data)})
	}
	writeJSON(w, http.StatusOK, d)
}

func download(w http.ResponseWriter, filename string, size int) {
	// Always an opaque download: the content is attacker-controlled and this
	// is the same origin as the UI, so it must never be rendered inline.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Length", strconv.Itoa(size))
	w.Header().Set("Cache-Control", "no-store")
}

func (a *api) raw(w http.ResponseWriter, r *http.Request) {
	m, ok := a.Inboxes.Get(r.PathValue("addr"), r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	download(w, "message-"+strconv.Itoa(m.Seq)+".eml", len(m.Raw))
	_, _ = w.Write(m.Raw)
}

func (a *api) attachment(w http.ResponseWriter, r *http.Request) {
	m, ok := a.Inboxes.Get(r.PathValue("addr"), r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	idx, err := strconv.Atoi(r.PathValue("aid"))
	p := mailparse.Parse(m.Raw)
	if err != nil || idx < 0 || idx >= len(p.Attachments) {
		writeErr(w, http.StatusNotFound, "attachment not found")
		return
	}
	at := p.Attachments[idx]
	name := at.Filename
	if name == "" {
		name = "attachment-" + strconv.Itoa(idx)
	}
	download(w, name, len(at.Data))
	_, _ = w.Write(at.Data)
}
