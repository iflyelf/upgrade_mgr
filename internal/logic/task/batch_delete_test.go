package task

import (
	"context"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// TestBatchDeleteValidation 空列表与超上限在访问数据库前直接拒绝。
func TestBatchDeleteValidation(t *testing.T) {
	s := &Service{}
	sc := scope.New(1, false)
	if _, err := s.BatchDelete(context.Background(), sc, nil); err == nil {
		t.Fatal("空列表应报错")
	}
	ids := make([]int64, MaxBatchDelete+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if _, err := s.BatchDelete(context.Background(), sc, ids); err == nil {
		t.Fatal("超过上限应报错")
	}
	// 非法 ID 与重复 ID 被忽略，不会触达数据库。
	res, err := s.BatchDelete(context.Background(), sc, []int64{0, -1})
	if err != nil {
		t.Fatalf("非法 ID 不应整体报错: %v", err)
	}
	if len(res.Deleted) != 0 || len(res.Failed) != 0 {
		t.Fatalf("非法 ID 应被忽略: %+v", res)
	}
}
