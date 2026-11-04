package main

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A SIGHUP must reload the configuration exactly once, so a proxy-list sync
// script can refresh the runtime without a WebUI password or a restart. The
// loop must also stop when its context is cancelled, so shutdown is clean.
func TestReloadLoopAppliesSIGHUP(t *testing.T) {
	var calls atomic.Int32
	signals := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		reloadLoop(ctx, signals, func() error {
			calls.Add(1)
			return nil
		}, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
		close(done)
	}()

	signals <- syscall.SIGHUP
	deadline := time.After(2 * time.Second)
	for calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("SIGHUP did not trigger a reload")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("reloads = %d, want 1", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reloadLoop did not stop after context cancellation")
	}
}
