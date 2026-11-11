package main

import (
	"context"
	"testing"
)

func TestStartMaintenanceWorkerNormalMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan struct{})
	done := startMaintenanceWorker(ctx, false, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(stopped)
	})
	if done == nil {
		cancel()
		t.Fatal("normal mode did not start the maintenance worker")
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("maintenance context ended before worker started")
	}
	cancel()
	<-stopped
	<-done
}

func TestStartMaintenanceWorkerPlaywrightMode(t *testing.T) {
	started := false
	if done := startMaintenanceWorker(context.Background(), true, func(context.Context) {
		started = true
	}); done != nil {
		t.Fatal("Playwright mode reported that maintenance was started")
	}
	if started {
		t.Fatal("Playwright mode invoked the maintenance worker")
	}
}
