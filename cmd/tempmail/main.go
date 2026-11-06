// Command tempmail runs the disposable-mail service: an SMTP receiver, a
// JSON API and a web UI, all backed by an in-memory store.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"tempmail/internal/config"
	"tempmail/internal/httpapi"
	"tempmail/internal/inbox"
	"tempmail/internal/ratelimit"
	"tempmail/internal/smtpd"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Fatalf("invalid configuration:\n%v", err)
	}

	// Make the GC work harder before the store cap plus transient parse
	// buffers could push the process over its container limit.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(int64(cfg.MaxStoreBytes)*5/4 + 64<<20)
	}

	store := inbox.New(inbox.Config{
		Domain:          cfg.Domain,
		TTL:             cfg.TTL,
		MaxInboxes:      cfg.MaxInboxes,
		MaxMsgsPerInbox: cfg.MaxMsgsPerInbox,
		MaxStoreBytes:   cfg.MaxStoreBytes,
	})
	createLimit := ratelimit.New(cfg.CreatePerMin, time.Minute)
	apiLimit := ratelimit.New(cfg.APIPerMin, time.Minute)

	sweepers := []*ratelimit.Limiter{createLimit, apiLimit}

	smtpOpts := smtpd.Options{
		Domain:            cfg.Domain,
		MaxMsgBytes:       cfg.MaxMsgBytes,
		MaxConns:          cfg.SMTPMaxConns,
		MaxConnsPerIP:     cfg.SMTPMaxConnsPerIP,
		MaxConcurrentData: cfg.SMTPMaxData,
	}
	if cfg.AutoCreateOnMail {
		autoLimit := ratelimit.New(cfg.AutoCreatePerMin, time.Minute)
		smtpOpts.AutoCreate = autoLimit
		sweepers = append(sweepers, autoLimit)
	}

	go func() { // janitor: reclaim expired inboxes and idle limiter keys
		for range time.Tick(10 * time.Second) {
			store.Reap()
			for _, l := range sweepers {
				l.Sweep()
			}
		}
	}()

	smtpSrv := smtpd.New(store, smtpOpts)
	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewHandler(httpapi.Options{
			Inboxes:     store,
			CreateLimit: createLimit,
			APILimit:    apiLimit,
			TrustProxy:  cfg.TrustProxy,
			ExposeStats: cfg.ExposeStats,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second, // > 30s long-poll
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	l, err := net.Listen("tcp", cfg.SMTPAddr)
	if err != nil {
		log.Fatalf("smtp listen: %v", err)
	}
	go func() {
		log.Printf("smtp listening on %s for @%s (ttl %s)", cfg.SMTPAddr, cfg.Domain, cfg.TTL)
		if err := smtpSrv.Serve(l); err != nil {
			log.Printf("smtp stopped: %v", err)
		}
	}()
	go func() {
		log.Printf("http listening on %s", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	_ = smtpSrv.Close()
}
