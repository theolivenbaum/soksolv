package main

import (
	"sync"
	"sync/atomic"
	"testing"

	"soksolv/reverse"
)

func TestElasticMemFESSOverBudget(t *testing.T) {
	origElastic := ElasticMem
	origPoolCap := PoolCap.Load()
	origBorrowed := elasticBorrowed.Load()
	origMemLimit := memLimitGlobal.Load()
	defer func() {
		ElasticMem = origElastic
		PoolCap.Store(origPoolCap)
		elasticBorrowed.Store(origBorrowed)
		memLimitGlobal.Store(origMemLimit)
		clearElasticBorrowed()
	}()

	clearElasticBorrowed()
	memLimitGlobal.Store(1000 << 20) // 1000 MiB limit

	var b atomic.Uint64
	b.Store(100 << 20) // 100 MiB nominal

	s := &solver{
		laneID:   1,
		budgetP:  &b,
		memLimit: 1000 << 20,
	}

	// Case 1: ElasticMem = false -> strictly nominal
	ElasticMem = false
	if s.overBudget(50 << 20) {
		t.Errorf("expected 50 MiB <= 100 MiB not to be over budget")
	}
	if !s.overBudget(150 << 20) {
		t.Errorf("expected 150 MiB > 100 MiB to be over budget when ElasticMem is false")
	}

	// Case 2: ElasticMem = true with PoolCap = 50 MiB
	ElasticMem = true
	PoolCap.Store(50 << 20) // 50 MiB elastic pool cap

	// Borrowing 20 MiB (120 MiB total needed) should succeed
	if s.overBudget(120 << 20) {
		t.Errorf("expected 120 MiB (borrowing 20 MiB from 50 MiB pool) to succeed")
	}
	if s.elasticHold != 20<<20 {
		t.Errorf("expected s.elasticHold == 20 MiB, got %d", s.elasticHold)
	}
	if elasticBorrowed.Load() != 20<<20 {
		t.Errorf("expected elasticBorrowed == 20 MiB, got %d", elasticBorrowed.Load())
	}

	// Expanding to 40 MiB borrowed (140 MiB total) should succeed
	if s.overBudget(140 << 20) {
		t.Errorf("expected 140 MiB (borrowing 40 MiB from 50 MiB pool) to succeed")
	}
	if s.elasticHold != 40<<20 {
		t.Errorf("expected s.elasticHold == 40 MiB, got %d", s.elasticHold)
	}

	// Trying to borrow 60 MiB (160 MiB total) exceeds 50 MiB cap -> should fail
	if !s.overBudget(160 << 20) {
		t.Errorf("expected 160 MiB (exceeding 50 MiB pool cap) to fail")
	}

	// Dropping back to nominal should release elastic tokens
	if s.overBudget(90 << 20) {
		t.Errorf("expected 90 MiB to be within nominal budget")
	}
	if s.elasticHold != 0 {
		t.Errorf("expected s.elasticHold to be released to 0, got %d", s.elasticHold)
	}
	if elasticBorrowed.Load() != 0 {
		t.Errorf("expected elasticBorrowed to be 0, got %d", elasticBorrowed.Load())
	}
}

func TestElasticPoolConcurrentAcquireRelease(t *testing.T) {
	origElastic := ElasticMem
	origPoolCap := PoolCap.Load()
	defer func() {
		ElasticMem = origElastic
		PoolCap.Store(origPoolCap)
		clearElasticBorrowed()
	}()

	ElasticMem = true
	clearElasticBorrowed()
	capBytes := int64(20 << 20) // 20 MiB cap
	PoolCap.Store(capBytes)

	const goroutines = 16
	const iterations = 500
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		laneID := g % 8
		go func(lid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				amount := uint64(1 << 20) // 1 MiB
				if acquireElastic(lid, amount) {
					cur := elasticBorrowed.Load()
					if cur > capBytes {
						t.Errorf("invariant violated: borrowed %d > cap %d", cur, capBytes)
					}
					releaseElastic(lid, amount)
					curAfter := elasticBorrowed.Load()
					if curAfter < 0 {
						t.Errorf("invariant violated: borrowed %d < 0", curAfter)
					}
				}
			}
		}(laneID)
	}
	wg.Wait()

	if elasticBorrowed.Load() != 0 {
		t.Errorf("expected final elasticBorrowed == 0, got %d", elasticBorrowed.Load())
	}
}

func TestElasticMemReverseHeadroom(t *testing.T) {
	origElastic := reverse.ElasticMem
	origAcquire := reverse.AcquireElastic
	origRelease := reverse.ReleaseElastic
	defer func() {
		reverse.ElasticMem = origElastic
		reverse.AcquireElastic = origAcquire
		reverse.ReleaseElastic = origRelease
	}()

	reverse.ElasticMem = true
	var acquired atomic.Uint64
	reverse.AcquireElastic = func(laneID int, bytes uint64) bool {
		acquired.Add(bytes)
		return true
	}
	reverse.ReleaseElastic = func(laneID int, bytes uint64) {
		acquired.Store(0)
	}

	if !reverse.ElasticMem {
		t.Errorf("expected reverse.ElasticMem to be true")
	}
	if !reverse.AcquireElastic(1, 10<<20) {
		t.Errorf("expected reverse.AcquireElastic to return true")
	}
	if acquired.Load() != 10<<20 {
		t.Errorf("expected 10 MiB acquired, got %d", acquired.Load())
	}
}
