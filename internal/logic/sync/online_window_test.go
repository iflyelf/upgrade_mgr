package sync

import (
	"testing"
	"time"
)

func TestOnlineProtectWindow(t *testing.T) {
	s := NewSyncer(nil)
	if got := s.onlineProtectWindow(); got != 15*time.Minute {
		t.Fatalf("默认窗口 = %v, want 15m", got)
	}
	s.SetOfflineAfter(func() string { return "30m" })
	if got := s.onlineProtectWindow(); got != 30*time.Minute {
		t.Fatalf("配置 30m 窗口 = %v, want 30m", got)
	}
	s.SetOfflineAfter(func() string { return "1s" })
	if got := s.onlineProtectWindow(); got != 15*time.Minute {
		t.Fatalf("过小阈值应回退 15m，实际 %v", got)
	}
	s.SetOfflineAfter(func() string { return "not-a-duration" })
	if got := s.onlineProtectWindow(); got != 15*time.Minute {
		t.Fatalf("非法阈值应回退 15m，实际 %v", got)
	}
}
