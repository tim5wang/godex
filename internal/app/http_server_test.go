package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBindHTTPServerContextCancelsActiveRequests(t *testing.T) {
	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})

	server := httptest.NewUnstartedServer(BindHTTPServerContext(parentCtx, &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(requestStarted)
			<-r.Context().Done()
			close(requestCanceled)
		}),
	}).Handler)
	server.Config = BindHTTPServerContext(parentCtx, server.Config)
	server.Start()
	defer server.Close()

	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get(server.URL)
		if err == nil && resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		errCh <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request to start")
	}

	cancel()

	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request cancellation")
	}

	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for client to observe closed request")
	}
}
