package buddy

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChangeSubscribersAreBounded(t *testing.T) {
	var changes changes
	ch, stop := changes.subscribe()
	for range 10000 {
		changes.publish()
	}
	if len(ch) != 1 {
		t.Fatalf("queued %d invalidations", len(ch))
	}
	<-ch
	stop()
	changes.publish()
	if len(ch) != 0 {
		t.Fatal("unsubscribed client received an event")
	}
}

func TestEventsFlushAndOverrideResponseDeadline(t *testing.T) {
	var changes changes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveEvents(w, r, &changes, "instance") }))
	server.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	server.Config.WriteTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(response.Header)
	}
	scan := bufio.NewScanner(response.Body)
	event := func() {
		t.Helper()
		for _, want := range []string{"event: change", "data: instance", ""} {
			if !scan.Scan() || scan.Text() != want {
				t.Fatalf("stream: %q, %v; want %q", scan.Text(), scan.Err(), want)
			}
		}
	}
	event()
	time.Sleep(40 * time.Millisecond) // Past the server's original write deadline.
	changes.publish()
	event()
	cancel()
	if scan.Scan() {
		t.Fatal("stream did not close on shutdown")
	}
	deadline := time.Now().Add(time.Second)
	for {
		changes.mu.Lock()
		n := len(changes.subscribers)
		changes.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscriber leaked after cancellation")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEventsHeartbeatOverHTTP2AndDisconnect(t *testing.T) {
	var changes changes
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveEvents(w, r, &changes, "instance") }))
	server.EnableHTTP2 = true
	server.Config.WriteTimeout = time.Second
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	client.Timeout = 20 * time.Second
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatal("expected HTTP/2", response.Proto)
	}
	scan := bufio.NewScanner(response.Body)
	for _, want := range []string{"event: change", "data: instance", "", "event: heartbeat", "data: instance", ""} {
		if !scan.Scan() || scan.Text() != want {
			t.Fatalf("stream: %q, %v; want %q", scan.Text(), scan.Err(), want)
		}
	}
	response.Body.Close()
	deadline := time.Now().Add(time.Second)
	for {
		changes.mu.Lock()
		n := len(changes.subscribers)
		changes.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscriber leaked after browser disconnected")
		}
		time.Sleep(time.Millisecond)
	}
}
