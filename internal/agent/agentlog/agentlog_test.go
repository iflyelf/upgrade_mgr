package agentlog

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer 供并发跟随测试安全读取输出。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestWriterRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	w, err := Open(path, 100, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte(strings.Repeat("a", 60))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(strings.Repeat("b", 60))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(strings.Repeat("c", 60))); err != nil {
		t.Fatal(err)
	}
	if err := w.LastRotateError(); err != nil {
		t.Fatalf("日志切割失败: %v", err)
	}

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取 %s: %v", name, err)
		}
		return string(b)
	}
	if got := read("agent.log"); got != strings.Repeat("c", 60) {
		t.Fatalf("当前日志应为最新内容，got %q", got)
	}
	if got := read("agent.log.1"); got != strings.Repeat("b", 60) {
		t.Fatalf(".1 应为上一份内容，got %q", got)
	}
	if got := read("agent.log.2"); got != strings.Repeat("a", 60) {
		t.Fatalf(".2 应为更早内容，got %q", got)
	}
}

func TestTailLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0640); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Tail(context.Background(), path, 2, false, &out); err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if got := out.String(); got != "two\nthree\n" {
		t.Fatalf("最后两行不符: %q", got)
	}
}

func TestTailFollowAndTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(path, []byte("old\n"), 0640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out lockedBuffer
	done := make(chan error, 1)
	go func() { done <- Tail(ctx, path, 0, true, &out) }()

	time.Sleep(100 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("new\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	waitContains(t, &out, "new\n")

	// 模拟 copytruncate：截断后写入新内容，跟随器应从文件头继续读取。
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("after-rotate\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	waitContains(t, &out, "after-rotate\n")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Tail 退出错误: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Tail 未在取消后退出")
	}
}

func waitContains(t *testing.T, b *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待日志 %q 超时，当前输出: %q", want, b.String())
}

func TestSetupWritesFile(t *testing.T) {
	old := log.Writer()
	defer log.SetOutput(old)
	path := filepath.Join(t.TempDir(), "agent.log")
	if err := Setup(path, 1<<20, 2); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer Close()
	log.Println("hello-agent-log")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "hello-agent-log") {
		t.Fatalf("日志文件未写入: %q", b)
	}
}
