package storage

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
)

// TestLocalSaveConcurrent 同一 key 并发保存时不能互相覆盖成半写文件。
func TestLocalSaveConcurrent(t *testing.T) {
	l, err := NewLocal(LocalConfig{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 32
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('a' + i%26)}, 64*1024+i)
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := l.Save("1/v1/artifact.bin", bytes.NewReader(payloads[i]), int64(len(payloads[i]))); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发保存失败: %v", err)
	}

	rc, err := l.Get("1/v1/artifact.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, p := range payloads {
		if bytes.Equal(got, p) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("文件内容被并发写入破坏（len=%d）", len(got))
	}
}

func TestLocalRejectsTraversal(t *testing.T) {
	l, err := NewLocal(LocalConfig{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../evil", "a/../../evil", "/etc/passwd", `C:\evil`} {
		if _, err := l.Save(key, bytes.NewReader([]byte("x")), 1); err == nil {
			t.Fatalf("key %q 应被拒绝", key)
		}
	}
	// 相邻目录（base 前缀相同）不能被当成 base 内路径。
	sibling := l.baseDir + "-evil"
	if _, err := l.Save(fmt.Sprintf("../%s/x", sibling), bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("越界路径应被拒绝")
	}
}
