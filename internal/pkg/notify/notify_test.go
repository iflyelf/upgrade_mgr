package notify

import (
	"os"
	"testing"
	"time"
)

func TestNewNoSocket(t *testing.T) {
	os.Unsetenv("NOTIFY_SOCKET")
	n, err := New()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != nil {
		t.Fatal("expected nil notifier without NOTIFY_SOCKET")
	}
	// nil notifier 的方法应为安全空操作
	if err := n.Ready(); err != nil {
		t.Fatalf("nil.Ready err: %v", err)
	}
	if err := n.Watchdog(); err != nil {
		t.Fatalf("nil.Watchdog err: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("nil.Close err: %v", err)
	}
}

func TestWatchdogIntervalDisabled(t *testing.T) {
	os.Unsetenv("WATCHDOG_USEC")
	if d := WatchdogInterval(); d != 0 {
		t.Fatalf("want 0, got %v", d)
	}
}

func TestWatchdogInterval(t *testing.T) {
	os.Setenv("WATCHDOG_USEC", "60000000") // 60s
	os.Unsetenv("WATCHDOG_PID")
	defer os.Unsetenv("WATCHDOG_USEC")
	if d := WatchdogInterval(); d != 30*time.Second {
		t.Fatalf("want 30s, got %v", d)
	}
}
