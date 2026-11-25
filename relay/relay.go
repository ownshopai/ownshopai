// Package relay keeps a shop reachable at <name>.ownshopai.store without an inbound port.
//
// The shop opens ONE outbound websocket to the OwnShop link service
// (GET wss://linksapi.ownshopai.store/v1/relay?name=<name>, Authorization: Bearer <app key>,
// subprotocol "ownshop-relay.v1"; contract: linkservice/API.md section 3a). The websocket's binary
// messages form one byte stream; on it runs a yamux session in which the link service is the
// CLIENT and the shop is the SERVER. Every visitor request arrives as one yamux stream carrying one
// HTTP/1.1 request, served by Config.Handler (http.Serve on the session). The handler must be the
// public-only web chat: whatever it serves is on the internet.
//
// Usage:
//
//	c := relay.New(relay.Config{URL: "wss://linksapi.ownshopai.store/v1/relay", Name: "rosa-bakery",
//		AppKey: func() string { return key }, Handler: publicHandler,
//		OnState: func(s relay.Status) { ... }})
//	c.Start(ctx)   // dials and keeps redialing until Stop or ctx ends
//	c.Kick()       // network changed: drop the connection and redial now
//	c.Stop(ctx)    // close; in-flight requests drain until ctx's deadline
//
// States: Connecting -> Online -> (connection lost) Connecting ... ; Offline while waiting to retry
// after a failure; Stopped when Stop was called or the service refused the link before the upgrade
// (401/403/404: retrying cannot help until the owner acts; Status.LastErr says why).
//
// Dead connections (laptop sleep, Android doze, NAT timeouts) are detected by yamux keepalive
// (Config.PingInterval, default 30 s) and a write timeout of 10 s; then the client redials with
// backoff from 1 s doubling to Config.MaxBackoff (default 60 s) with jitter. A connection that
// stayed up at least 10 s resets the backoff and is redialed at once when it ends; a shorter one
// counts as a failure (so two shops fighting over one name, each replacing the other, back off). There is no resumable session in v1: the link service holds a
// visitor request up to 15 s waiting for the shop to come back, so a quick redial loses nothing.
//
// OnState is called on its own goroutine, in order, never blocking the connection. Kick and Stop
// are safe from any goroutine and idempotent. A Client is single-use: after Stop, make a new one. The package has no mutable package-level state and no
// cgo; it builds for android, linux, windows and darwin.
package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// stableAfter is how long a connection must live to count as good.
const stableAfter = 10 * time.Second

// Subprotocol is the websocket subprotocol of the relay contract.
const Subprotocol = "ownshop-relay.v1"

type State string

const (
	Connecting State = "connecting"
	Online     State = "online"
	Offline    State = "offline" // waiting to retry
	Stopped    State = "stopped"
)

// Status is a snapshot handed to OnState and returned by Client.Status.
type Status struct {
	State   State
	Since   time.Time     // when State began
	Attempt int           // failed dials since the last good connection
	LastErr string        // why the last connection failed or ended; "" when none
	RTT     time.Duration // last measured round trip while Online
	// LastConnected is when the last connection came up (zero if never).
	LastConnected time.Time
}

type Config struct {
	URL          string        // wss://host/v1/relay (name is added as ?name=)
	Name         string        // the claimed link name
	AppKey       func() string // read on every dial (sign-in may change it)
	Handler      http.Handler  // the PUBLIC handler; never an owner UI
	OnState      func(Status)
	PingInterval time.Duration // default 30 s
	MaxBackoff   time.Duration // default 60 s
	HTTPClient   *http.Client  // optional, for the websocket handshake
}

// RefusedError is a refusal before the upgrade (401/403/404): retrying will not help.
type RefusedError struct {
	Status int
	Body   string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("link service refused the relay (HTTP %d) %s", e.Status, e.Body)
}

type Client struct {
	cfg Config

	mu      sync.Mutex
	st      Status
	cancel  context.CancelFunc
	kick    chan struct{}
	done    chan struct{}
	conn    io.Closer // current session, for Kick
	srv     *http.Server
	started bool

	events chan Status
}

func New(cfg Config) *Client {
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 30 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 60 * time.Second
	}
	c := &Client{cfg: cfg, kick: make(chan struct{}, 1), done: make(chan struct{}), events: make(chan Status, 64)}
	c.st = Status{State: Stopped, Since: time.Now()}
	c.srv = &http.Server{Handler: cfg.Handler, ReadHeaderTimeout: 30 * time.Second}
	go c.deliver()
	return c
}

// deliver calls OnState in order on its own goroutine.
func (c *Client) deliver() {
	for s := range c.events {
		if c.cfg.OnState != nil {
			c.cfg.OnState(s)
		}
	}
}

func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

// set changes the status under the lock and queues an event (dropped if the queue is full).
func (c *Client) set(f func(*Status)) {
	c.mu.Lock()
	old := c.st.State
	f(&c.st)
	if c.st.State != old {
		c.st.Since = time.Now()
	}
	s := c.st
	c.mu.Unlock()
	select {
	case c.events <- s:
	default:
	}
}

// Start begins dialing in the background. Calling it again is a no-op.
func (c *Client) Start(ctx context.Context) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	ctx, c.cancel = context.WithCancel(ctx)
	c.mu.Unlock()
	go c.loop(ctx)
}

// Kick drops the current connection (if any) and redials now.
func (c *Client) Kick() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
	c.mu.Lock()
	cl := c.conn
	c.mu.Unlock()
	if cl != nil {
		cl.Close()
	}
}

// Stop closes the relay; in-flight requests get until ctx's deadline. Safe to call many times.
func (c *Client) Stop(ctx context.Context) {
	c.mu.Lock()
	cancel, started := c.cancel, c.started
	c.mu.Unlock()
	if !started {
		return
	}
	c.srv.Shutdown(ctx) // stops new streams, waits for the requests in flight (until ctx's deadline)
	if cancel != nil {
		cancel()
	}
	select {
	case <-c.done:
	case <-ctx.Done():
	}
}

func (c *Client) loop(ctx context.Context) {
	defer close(c.done)
	first := min(time.Second, c.cfg.MaxBackoff)
	backoff := first
	for {
		c.set(func(s *Status) { s.State = Connecting })
		up, lived, err := c.runOnce(ctx)
		good := up && lived >= stableAfter
		if ctx.Err() != nil {
			c.set(func(s *Status) { s.State = Stopped; s.LastErr = "" })
			return
		}
		var ref *RefusedError
		if errors.As(err, &ref) {
			c.set(func(s *Status) { s.State = Stopped; s.LastErr = err.Error(); s.Attempt++ })
			return
		}
		if good {
			backoff = first
		}
		msg := "connection closed"
		if err != nil {
			msg = err.Error()
		}
		c.set(func(s *Status) {
			s.State = Offline
			s.LastErr = msg
			if good {
				s.Attempt = 0
			} else {
				s.Attempt++
			}
		})
		wait := backoff/2 + rand.N(backoff/2+1) // jitter: 50-100% of the step
		if good {
			wait = 0 // first redial after a good connection is immediate
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			c.set(func(s *Status) { s.State = Stopped; s.LastErr = "" })
			return
		case <-c.kick:
			t.Stop()
			backoff = first
		case <-t.C:
			if !good {
				backoff *= 2
				if backoff > c.cfg.MaxBackoff {
					backoff = c.cfg.MaxBackoff
				}
			}
		}
	}
}

// runOnce dials, serves until the session ends, and says whether it got online and for how long.
func (c *Client) runOnce(ctx context.Context) (bool, time.Duration, error) {
	up, start, err := c.dialServe(ctx)
	if !up {
		return false, 0, err
	}
	return true, time.Since(start), err
}

func (c *Client) dialServe(ctx context.Context) (bool, time.Time, error) {
	// drain a stale kick so it does not cut the new connection
	select {
	case <-c.kick:
	default:
	}
	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		return false, time.Time{}, &RefusedError{Body: "bad relay URL"}
	}
	q := u.Query()
	q.Set("name", c.cfg.Name)
	u.RawQuery = q.Encode()
	key := ""
	if c.cfg.AppKey != nil {
		key = c.cfg.AppKey()
	}
	if key == "" {
		return false, time.Time{}, &RefusedError{Status: 401, Body: "not signed in"}
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, res, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{
		HTTPClient:   c.cfg.HTTPClient,
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + key}},
		Subprotocols: []string{Subprotocol},
	})
	cancel()
	if err != nil {
		if res != nil {
			switch res.StatusCode {
			case 401, 403, 404:
				var b []byte
				if res.Body != nil {
					b, _ = io.ReadAll(io.LimitReader(res.Body, 512))
				}
				return false, time.Time{}, &RefusedError{Status: res.StatusCode, Body: string(b)}
			}
		}
		return false, time.Time{}, err
	}
	ws.SetReadLimit(-1) // the byte stream is chunked by yamux; frames can be any size
	nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)

	ycfg := yamux.DefaultConfig()
	ycfg.EnableKeepAlive = true
	ycfg.KeepAliveInterval = c.cfg.PingInterval
	ycfg.ConnectionWriteTimeout = 10 * time.Second
	ycfg.LogOutput = io.Discard
	sess, err := yamux.Server(nc, ycfg)
	if err != nil {
		nc.Close()
		return false, time.Time{}, err
	}
	c.mu.Lock()
	c.conn = sess
	c.mu.Unlock()
	now := time.Now()
	c.set(func(s *Status) { s.State = Online; s.LastErr = ""; s.Attempt = 0; s.LastConnected = now })

	stopPing := make(chan struct{})
	go func() { // RTT for the status; keepalive itself is yamux's
		t := time.NewTicker(c.cfg.PingInterval)
		defer t.Stop()
		for {
			if rtt, err := sess.Ping(); err == nil {
				c.set(func(s *Status) { s.RTT = rtt })
			}
			select {
			case <-stopPing:
				return
			case <-t.C:
			}
		}
	}()
	go func() { // Stop or ctx end closes the session
		select {
		case <-ctx.Done():
			sess.Close()
		case <-sess.CloseChan():
		}
	}()
	err = c.srv.Serve(newSessListener(sess))
	close(stopPing)
	if errors.Is(err, http.ErrServerClosed) {
		// Stop: Shutdown is draining the requests in flight on this session; Stop cancels ctx when
		// they are done (or its deadline passed), then the session closes.
		<-ctx.Done()
	}
	sess.Close()
	c.mu.Lock()
	c.conn = nil
	c.mu.Unlock()
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) || errors.Is(err, yamux.ErrSessionShutdown) {
		err = nil
	}
	return true, now, err
}

// sessListener serves a yamux session as a net.Listener. Close (from http.Server.Shutdown) only
// stops new streams (yamux GoAway) and ends Accept; the session and its streams in flight live on
// until runOnce closes it, so Shutdown can drain them. Accept errors are permanent (http.Server
// retries temporary ones forever).
type sessListener struct {
	sess *yamux.Session
	done chan struct{}
	once sync.Once
}

func newSessListener(s *yamux.Session) *sessListener {
	return &sessListener{sess: s, done: make(chan struct{})}
}

func (l *sessListener) Accept() (net.Conn, error) {
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := l.sess.Accept()
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, yamux.ErrSessionShutdown
		}
		return r.c, nil
	case <-l.done:
		go func() { // a stream accepted after Close is refused
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, net.ErrClosed
	}
}

func (l *sessListener) Close() error {
	l.once.Do(func() { l.sess.GoAway(); close(l.done) })
	return nil
}

func (l *sessListener) Addr() net.Addr { return l.sess.Addr() }
