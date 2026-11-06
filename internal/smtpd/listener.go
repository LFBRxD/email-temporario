package smtpd

import (
	"net"
	"sync"
)

// limitListener caps concurrent connections, overall and per remote IP, so
// slow or idle clients cannot exhaust file descriptors or goroutines.
type limitListener struct {
	net.Listener
	maxTotal, maxPerIP int

	mu    sync.Mutex
	total int
	perIP map[string]int
}

func newLimitListener(l net.Listener, maxTotal, maxPerIP int) *limitListener {
	return &limitListener{Listener: l, maxTotal: maxTotal, maxPerIP: maxPerIP, perIP: map[string]int{}}
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := remoteIP(c)
		l.mu.Lock()
		if l.total >= l.maxTotal || l.perIP[ip] >= l.maxPerIP {
			l.mu.Unlock()
			_, _ = c.Write([]byte("421 4.7.0 too many connections\r\n"))
			_ = c.Close()
			continue
		}
		l.total++
		l.perIP[ip]++
		l.mu.Unlock()
		return &trackedConn{Conn: c, l: l, ip: ip}, nil
	}
}

func (l *limitListener) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.perIP[ip]--; l.perIP[ip] <= 0 {
		delete(l.perIP, ip)
	}
}

type trackedConn struct {
	net.Conn
	l    *limitListener
	ip   string
	once sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.l.release(c.ip) })
	return c.Conn.Close()
}

func remoteIP(c net.Conn) string {
	h, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return c.RemoteAddr().String()
	}
	return h
}
