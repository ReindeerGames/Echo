package observer

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestTCPProbeConnectsWithoutSendingApplicationData(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	type acceptedResult struct {
		data []byte
		err  error
	}
	accepted := make(chan acceptedResult, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			accepted <- acceptedResult{err: acceptErr}
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		buffer := make([]byte, 1)
		count, readErr := connection.Read(buffer)
		if errors.Is(readErr, io.EOF) {
			readErr = nil
		}
		accepted <- acceptedResult{data: buffer[:count], err: readErr}
	}()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	var received domain.MonitorEvent
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer echo.Close()
	service, err := New(Config{EchoURL: echo.URL, Source: "observer", AllowInsecureHTTP: true, TCPProbes: []TCPProbe{{Name: "database", ServiceID: "svc_database", Host: "127.0.0.1", Port: port}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := <-accepted
	if result.err != nil || len(result.data) != 0 {
		t.Fatalf("TCP probe exchanged application data: data=%q err=%v", result.data, result.err)
	}
	if received.Status != "healthy" || received.Severity != "info" || received.Fingerprint != "tcp-connectivity" || !strings.Contains(received.Summary, "port "+portText) || strings.Contains(string(received.Details), "127.0.0.1") {
		t.Fatalf("unexpected TCP evidence: %#v", received)
	}
}

func TestTCPProbeFailureIsBoundedAndDoesNotExposeHost(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{EchoURL: "http://echo.test", Source: "observer", AllowInsecureHTTP: true, TCPProbes: []TCPProbe{{Name: "database", ServiceID: "svc_database", Host: "127.0.0.1", Port: port}}}, "observer-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	event := service.observeTCP(t.Context(), service.config.TCPProbes[0])
	if event.Status != "unhealthy" || event.Severity != "critical" || event.Fingerprint != "tcp-connectivity" || !strings.Contains(string(event.Details), `"failure":"connection_failed"`) || strings.Contains(string(event.Details), "127.0.0.1") {
		t.Fatalf("unexpected failed TCP evidence: %#v", event)
	}
}
