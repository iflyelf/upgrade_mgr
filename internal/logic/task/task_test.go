package task

import (
	"reflect"
	"testing"
)

func TestSplitBatches(t *testing.T) {
	cases := []struct {
		name  string
		total int
		st    Strategy
		want  []int
	}{
		{"single batch default", 5, Strategy{}, []int{5}},
		{"canary then rest", 10, Strategy{Mode: "canary", CanaryCount: 2, BatchSize: 4}, []int{2, 4, 4}},
		{"count batches", 10, Strategy{Mode: "count", BatchSize: 3}, []int{3, 3, 3, 1}},
		{"percent 20%", 10, Strategy{Mode: "percent", BatchSize: 20}, []int{2, 2, 2, 2, 2}},
		{"percent 20% small total", 3, Strategy{Mode: "percent", BatchSize: 20}, []int{1, 1, 1}},
		{"percent 50% odd total", 5, Strategy{Mode: "percent", BatchSize: 50}, []int{2, 2, 1}},
		{"percent 10% edge", 8, Strategy{Mode: "percent", BatchSize: 10}, []int{1, 1, 1, 1, 1, 1, 1, 1}},
		{"canary bigger than total", 3, Strategy{Mode: "canary", CanaryCount: 5}, []int{3}},
		{"zero total", 0, Strategy{}, nil},
	}
	for _, c := range cases {
		got := splitBatches(c.total, c.st)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: splitBatches(%d,%+v)=%v want %v", c.name, c.total, c.st, got, c.want)
		}
	}
}
