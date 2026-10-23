package telemetry

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// captureStderr runs f and returns what it wrote to os.Stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	f()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestSetupExports(t *testing.T) {
	got := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- r.URL.Path + " " + string(body)
	}))
	defer srv.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("OTEL_SDK_DISABLED", "")

	logger, shutdown := Setup(context.Background())
	logger.Info("hello from the test")
	shutdown()

	select {
	case req := <-got:
		if want := "/v1/logs "; len(req) < len(want) || req[:len(want)] != want {
			t.Errorf("collector got %.80q, want a request to /v1/logs", req)
		}
	default:
		t.Fatal("nothing was exported")
	}
}

func TestSetupDisabled(t *testing.T) {
	tests := []map[string]string{
		{"OTEL_EXPORTER_OTLP_ENDPOINT": "", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": ""},
		{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:1", "OTEL_SDK_DISABLED": "true"},
	}
	for _, env := range tests {
		for k, v := range env {
			t.Setenv(k, v)
		}
		logger, shutdown := Setup(context.Background())
		if logger.Enabled(context.Background(), 0) {
			t.Errorf("env %v: the logger is enabled, want it to discard", env)
		}
		shutdown()
	}
}

// An unreachable collector must neither print anything nor hold up the exit
// for longer than shutdownTimeout.
func TestSetupUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+addr)
	t.Setenv("OTEL_SDK_DISABLED", "")

	var took time.Duration
	out := captureStderr(t, func() {
		logger, shutdown := Setup(context.Background())
		logger.Info("lost")
		start := time.Now()
		shutdown()
		took = time.Since(start)
	})
	if out != "" {
		t.Errorf("stderr got %q, want nothing", out)
	}
	if took > shutdownTimeout+time.Second {
		t.Errorf("shutdown took %v, want at most about %v", took, shutdownTimeout)
	}
}
