package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunSuccess(t *testing.T) {
	res, err := Run(context.Background(), Spec{
		Argv:    []string{"sh", "-c", "exit 0"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
}

func TestRunNonZero(t *testing.T) {
	res, err := Run(context.Background(), Spec{
		Argv:    []string{"sh", "-c", "exit 3"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("non-zero exit should not be framework error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit=%d want 3", res.ExitCode)
	}
}

func TestRunStdout(t *testing.T) {
	res, err := Run(context.Background(), Spec{
		Argv:    []string{"echo", "hello"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Stdout != "hello\n" {
		t.Fatalf("stdout=%q", res.Stdout)
	}
}

func TestRunTimeout(t *testing.T) {
	res, err := Run(context.Background(), Spec{
		Argv:    []string{"sleep", "5"},
		Timeout: 200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut, res=%+v", res)
	}
}

func TestRunNotFound(t *testing.T) {
	_, err := Run(context.Background(), Spec{
		Argv:    []string{"/nonexistent/definitely-not-here-xyz"},
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected start failure")
	}
}

func TestRunEmpty(t *testing.T) {
	if _, err := Run(context.Background(), Spec{}); err != ErrEmptyArgv {
		t.Fatalf("want ErrEmptyArgv, got %v", err)
	}
}

func TestRunEnv(t *testing.T) {
	res, err := Run(context.Background(), Spec{
		Argv:    []string{"sh", "-c", "echo $UPGRADE_TEST_VAR"},
		Env:     []string{"UPGRADE_TEST_VAR=xyz"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Stdout != "xyz\n" {
		t.Fatalf("stdout=%q", res.Stdout)
	}
}

func TestCheck(t *testing.T) {
	if !Check(context.Background(), Spec{Argv: []string{"true"}, Timeout: time.Second}) {
		t.Fatal("true should pass")
	}
	if Check(context.Background(), Spec{Argv: []string{"false"}, Timeout: time.Second}) {
		t.Fatal("false should fail")
	}
}

// TestCappedBuffer 输出缓冲截断：最多保留 max 字节并追加提示。
func TestCappedBuffer(t *testing.T) {
	c := &cappedBuffer{max: 5}
	n, err := c.Write([]byte("abcdefghij"))
	if err != nil || n != 10 {
		t.Fatalf("Write 应返回原始长度 n=%d err=%v", n, err)
	}
	out := c.String()
	if !strings.Contains(out, "abcde") || !strings.Contains(out, "已截断") {
		t.Fatalf("截断内容不符: %q", out)
	}
	if !c.truncated {
		t.Fatal("应标记 truncated")
	}

	// 未超限不追加提示
	c2 := &cappedBuffer{max: 10}
	c2.Write([]byte("12345"))
	if strings.Contains(c2.String(), "已截断") {
		t.Fatal("未超限不应提示截断")
	}
}
