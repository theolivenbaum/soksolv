package trace

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestConcurrentSnapshotPublication(t *testing.T) {
	r := New()
	r.Root(100)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			r.Step(i, 100-i, 0)
			r.SetExtra("live", i)
			r.Finish("timeout", i, i, 0)
		}
	}()
	for i := 0; i < 1000; i++ {
		s := r.Snapshot()
		if _, err := json.Marshal(s); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
func TestSnapshotOwnsNestedValuesAndPhases(t *testing.T) {
	r := New()
	nested := map[string]any{"dropped": []int64{1, 2}}
	r.SetExtra("meet", nested)
	nested["dropped"].([]int64)[0] = 9
	r.Root(20)
	r.Step(1, 19, 0)
	r.Finish("timeout", 1, 1, 0)
	first := r.Snapshot()
	r.Step(2, 18, 0)
	r.Finish("solved", 2, 2, 0)
	second := r.Snapshot()
	if first.End != "timeout" || len(first.Improvements) != 1 || first.Improvements[0][2] != 19 || second.End != "solved" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	m := first.Extra["meet"].(map[string]any)
	if m["dropped"].([]int64)[0] != 1 {
		t.Fatal("input was not detached")
	}
	m["dropped"].([]int64)[1] = 99
	again := r.Snapshot()
	if again.Extra["meet"].(map[string]any)["dropped"].([]int64)[1] != 2 {
		t.Fatal("snapshot shared nested data")
	}
	first.Improvements[0][2] = 0
	if r.Snapshot().Improvements[0][2] != 19 {
		t.Fatal("snapshot shared event data")
	}
}

func BenchmarkSnapshot(b *testing.B) {
	r := New()
	r.Root(100)
	for i := 0; i < 24; i++ {
		r.Step(i, 99-i, i)
	}
	r.SetExtra("meet_back", map[string]any{"posted": int64(5), "confirmed": int64(2), "dropped": []int64{0, 0, 1, 0, 0, 0}})
	r.Finish("timeout", 1000, 500, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Snapshot()
	}
}
