// Package relaytest is a fake link-service relay endpoint for tests: it accepts the shop's
// websocket, runs the yamux CLIENT side and lets the test send HTTP requests to the shop.
package relaytest

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

type Server struct {
	*httptest.Server
	Key    string // the app key it accepts
	Name   string // the link name it accepts
	Status int    // when non-zero, refuse every dial with this status

	mu      sync.Mutex
	sess    *yamux.Session
	ready   chan struct{}
	accepts int
}

func New(key, name string) *Server {
	s := &Server{Key: key, Name: name, ready: make(chan struct{})}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// URL of the relay endpoint, ws://...
func (s *Server) RelayURL() string { return "ws" + strings.TrimPrefix(s.Server.URL, "http") + "/v1/relay" }

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	st := s.Status
	s.mu.Unlock()
	if st != 0 {
		http.Error(w, `{"error":{"code":"refused"}}`, st)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.Key {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.URL.Query().Get("name") != s.Name {
		http.Error(w, "link_not_found", 404)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"ownshop-relay.v1"}})
	if err != nil {
		return
	}
	ws.SetReadLimit(-1)
	nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	sess, err := yamux.Client(nc, cfg)
	if err != nil {
		return
	}
	s.mu.Lock()
	if s.sess != nil {
		s.sess.Close() // newer replaces older
	}
	s.sess = sess
	s.accepts++
	close(s.ready)
	s.ready = make(chan struct{})
	s.mu.Unlock()
	<-sess.CloseChan()
}

// Session waits for a live session (ctx bounds the wait).
func (s *Server) Session(ctx context.Context) (*yamux.Session, error) {
	for {
		s.mu.Lock()
		sess, ready := s.sess, s.ready
		s.mu.Unlock()
		if sess != nil && !sess.IsClosed() {
			return sess, nil
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, errors.New("no relay session")
		}
	}
}

// Drop closes the current session (the server side hangs up).
func (s *Server) Drop() {
	s.mu.Lock()
	if s.sess != nil {
		s.sess.Close()
	}
	s.mu.Unlock()
}

func (s *Server) SetStatus(code int) {
	s.mu.Lock()
	s.Status = code
	s.mu.Unlock()
}

// Do sends one request over a fresh stream, the way the link service does.
func (s *Server) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	sess, err := s.Session(ctx)
	if err != nil {
		return nil, err
	}
	st, err := sess.Open()
	if err != nil {
		return nil, err
	}
	req.Close = true
	if err := req.Write(st); err != nil {
		st.Close()
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(st), req)
}

// Accepts is how many relay connections were accepted.
func (s *Server) Accepts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepts
}
