package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type LifecycleService interface {
	Start(context.Context) error
	Stop(context.Context) error
}

type HTTPServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

// ServeRuntime owns one HTTP server plus the long-running services that must
// start before it and stop after it.
type ServeRuntime struct {
	Server          HTTPServer
	Services        []LifecycleService
	ShutdownTimeout time.Duration
}

func (r ServeRuntime) Run(ctx context.Context) error {
	if r.Server == nil {
		return fmt.Errorf("missing HTTP server")
	}
	timeout := r.ShutdownTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	started := make([]LifecycleService, 0, len(r.Services))
	for _, service := range r.Services {
		if service == nil {
			continue
		}
		if err := service.Start(ctx); err != nil {
			return errors.Join(err, stopServices(timeout, started))
		}
		started = append(started, service)
	}

	serverErrCh := make(chan error, 1)
	go func() {
		err := r.Server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErrCh <- err
	}()

	select {
	case <-ctx.Done():
		return stopRuntime(r.Server, timeout, started)
	case err := <-serverErrCh:
		return errors.Join(err, stopRuntime(r.Server, timeout, started))
	}
}

func stopRuntime(server HTTPServer, timeout time.Duration, started []LifecycleService) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var errs []error
	if server != nil {
		if err := server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, err)
		}
	}
	if err := stopServices(timeout, started); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func stopServices(timeout time.Duration, started []LifecycleService) error {
	if len(started) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var errs []error
	for i := len(started) - 1; i >= 0; i-- {
		if started[i] == nil {
			continue
		}
		if err := started[i].Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
