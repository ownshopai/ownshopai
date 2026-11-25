package relay_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ownshopai/ownshopai/relay"
	"github.com/ownshopai/ownshopai/relay/relaytest"
)

func waitState(t *testing.T, c *relay.Client, want relay.State) relay.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := c.Status(); s.State == want {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state %s, want %s (%s)", c.Status().State, want, c.Status().LastErr)
	return relay.Status{}
}

func get(t *testing.T, srv *relaytest.Server, path string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequest("GET", "http://shop.example"+path, nil)
	res, err := srv.Do(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func newClient(srv *relaytest.Server, key string) *relay.Client {
	h := http.NewServeMux()
	h.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hi "+r.Host) })
	return relay.New(relay.Config{URL: srv.RelayURL(), Name: "rosa", AppKey: func() string { return key }, Handler: h,
		PingInterval: time.Second, MaxBackoff: 200 * time.Millisecond})
}

func TestRelayServesAndReconnects(t *testing.T) {
	srv := relaytest.New("k", "rosa")
	defer srv.Close()
	c := newClient(srv, "k")
	c.Start(context.Background())
	waitState(t, c, relay.Online)
	if code, body := get(t, srv, "/hello"); code != 200 || body != "hi shop.example" {
		t.Fatalf("%d %q", code, body)
	}
	if code, _ := get(t, srv, "/orders"); code != 404 {
		t.Fatalf("unknown route %d", code)
	}
	srv.Drop()
	time.Sleep(50 * time.Millisecond)
	if code, _ := get(t, srv, "/hello"); code != 200 {
		t.Fatalf("after reconnect %d", code)
	}
	if srv.Accepts() < 2 {
		t.Fatalf("accepts %d", srv.Accepts())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.Stop(ctx)
	if s := c.Status(); s.State != relay.Stopped {
		t.Fatalf("after stop %s", s.State)
	}
	c.Stop(ctx) // idempotent
}

func TestRelayRefusedStopsRetrying(t *testing.T) {
	srv := relaytest.New("k", "rosa")
	defer srv.Close()
	srv.SetStatus(404)
	c := newClient(srv, "k")
	c.Start(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(c.Status().LastErr, "404") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	s := c.Status()
	if s.State != relay.Stopped || !strings.Contains(s.LastErr, "404") {
		t.Fatalf("%s %q", s.State, s.LastErr)
	}
	time.Sleep(300 * time.Millisecond)
	if srv.Accepts() != 0 || c.Status().Attempt != 1 {
		t.Fatalf("retried: attempt %d", c.Status().Attempt)
	}
}

func TestRelayKickRedials(t *testing.T) {
	srv := relaytest.New("k", "rosa")
	defer srv.Close()
	c := newClient(srv, "k")
	c.Start(context.Background())
	waitState(t, c, relay.Online)
	c.Kick()
	deadline := time.Now().Add(3 * time.Second)
	for srv.Accepts() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.Accepts() < 2 {
		t.Fatal("kick did not redial")
	}
	c.Stop(context.Background())
}

func TestRelayStopDrainsInFlight(t *testing.T) {
	srv := relaytest.New("k", "rosa")
	defer srv.Close()
	h := http.NewServeMux()
	started := make(chan struct{})
	h.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(500 * time.Millisecond)
		io.WriteString(w, "done")
	})
	c := relay.New(relay.Config{URL: srv.RelayURL(), Name: "rosa", AppKey: func() string { return "k" }, Handler: h})
	c.Start(context.Background())
	waitState(t, c, relay.Online)
	got := make(chan string, 1)
	go func() {
		code, body := get(t, srv, "/slow")
		got <- fmt.Sprint(code, " ", body)
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.Stop(ctx)
	if r := <-got; r != "200 done" {
		t.Fatalf("in-flight request cut by Stop: %q", r)
	}
}

// A server that accepts and drops at once (another shop replacing this one) must not get a tight loop.
func TestRelayShortConnectionsBackOff(t *testing.T) {
	srv := relaytest.New("k", "rosa")
	defer srv.Close()
	srv.DropOnAccept = true
	h := http.NewServeMux()
	c := relay.New(relay.Config{URL: srv.RelayURL(), Name: "rosa", AppKey: func() string { return "k" }, Handler: h,
		MaxBackoff: 400 * time.Millisecond})
	c.Start(context.Background())
	time.Sleep(1500 * time.Millisecond)
	c.Stop(context.Background())
	if n := srv.Accepts(); n > 8 {
		t.Fatalf("%d connections in 1.5 s: no backoff", n)
	}
}
