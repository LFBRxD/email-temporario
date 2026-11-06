// Package config reads and validates settings from the environment.
// Invalid values stop the process at start-up instead of silently falling
// back to defaults.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Domain   string
	HTTPAddr string
	SMTPAddr string
	TTL      time.Duration

	MaxInboxes      int
	MaxMsgsPerInbox int
	MaxMsgBytes     int
	MaxStoreBytes   int

	CreatePerMin int // inbox creations per IP per minute
	APIPerMin    int // API requests per IP per minute

	SMTPMaxConns      int // concurrent SMTP connections
	SMTPMaxConnsPerIP int
	SMTPMaxData       int // messages buffered/parsed at the same time

	// AutoCreateOnMail lets an e-mail for a valid, not-yet-existing name create
	// that inbox. Convenient for automation; lets senders consume inbox slots,
	// so it is off by default and rate limited per sender IP.
	AutoCreateOnMail bool
	AutoCreatePerMin int

	TrustProxy  bool // honour X-Forwarded-For (only behind a proxy that overwrites it)
	ExposeStats bool // serve GET /api/stats
}

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// Load reads settings through getenv (os.Getenv in production). DOMAIN is
// required: a wrong default would make the service accept the wrong domain.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	str := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	num := func(k string, def, min int) int {
		v := strings.TrimSpace(getenv(k))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < min {
			errs = append(errs, fmt.Errorf("%s must be an integer >= %d, got %q", k, min, v))
			return def
		}
		return n
	}
	boolean := func(k string) bool {
		v := strings.TrimSpace(getenv(k))
		if v == "" {
			return false
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s must be true or false, got %q", k, v))
		}
		return b
	}

	c := Config{
		Domain:            strings.ToLower(strings.TrimSpace(getenv("DOMAIN"))),
		HTTPAddr:          str("HTTP_ADDR", ":8080"),
		SMTPAddr:          str("SMTP_ADDR", ":2525"),
		TTL:               time.Duration(num("TTL_SECONDS", 300, 10)) * time.Second,
		MaxInboxes:        num("MAX_INBOXES", 20000, 1),
		MaxMsgsPerInbox:   num("MAX_MSGS_PER_INBOX", 30, 1),
		MaxMsgBytes:       num("MAX_MSG_BYTES", 5<<20, 1024),
		MaxStoreBytes:     num("MAX_STORE_MB", 1024, 1) << 20,
		CreatePerMin:      num("CREATE_PER_MIN", 10, 1),
		APIPerMin:         num("API_PER_MIN", 240, 1),
		SMTPMaxConns:      num("SMTP_MAX_CONNS", 256, 1),
		SMTPMaxConnsPerIP: num("SMTP_MAX_CONNS_PER_IP", 10, 1),
		SMTPMaxData:       num("SMTP_MAX_DATA", 32, 1),
		AutoCreateOnMail:  boolean("AUTO_CREATE_ON_MAIL"),
		AutoCreatePerMin:  num("AUTO_CREATE_PER_MIN", 60, 1),
		TrustProxy:        boolean("TRUST_PROXY"),
		ExposeStats:       boolean("EXPOSE_STATS"),
	}
	switch {
	case c.Domain == "":
		errs = append(errs, errors.New("DOMAIN is required (e.g. DOMAIN=mail.example.com)"))
	case !domainRe.MatchString(c.Domain):
		errs = append(errs, fmt.Errorf("DOMAIN %q is not a valid host name", c.Domain))
	}
	if c.MaxMsgBytes > c.MaxStoreBytes {
		errs = append(errs, errors.New("MAX_MSG_BYTES must not exceed MAX_STORE_MB"))
	}
	return c, errors.Join(errs...)
}
