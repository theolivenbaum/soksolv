package main

import (
	"sync/atomic"
)

// ElasticPoolPct (exp-710), when > 0, sets the elastic pool to this % of -mem
// instead of 72%-70%.
var ElasticPoolPct int

var (
	// elasticBorrowed tracks the total number of elastic bytes currently borrowed by all lanes combined.
	elasticBorrowed atomic.Int64

	// PoolCap is the process-wide maximum elastic headroom (resetElasticPool).
	PoolCap atomic.Int64

	// memLimitGlobal holds the current process memory limit in bytes.
	memLimitGlobal atomic.Uint64

	// laneBorrowed tracks borrowed bytes per laneID (0..31).
	laneBorrowed [32]atomic.Uint64
)

// acquireElastic attempts to borrow bytes of elastic headroom for laneID.
// If elasticBorrowed + bytes <= PoolCap AND !mappedOver(memLimit, 75),
// atomically adds bytes to elasticBorrowed and records the lane's borrowed amount, returning true.
// Otherwise returns false.
func acquireElastic(laneID int, bytes uint64) bool {
	if !ElasticMem || bytes == 0 {
		return false
	}
	lim := memLimitGlobal.Load()
	if lim > 0 && mappedOver(lim, 75) {
		return false
	}
	cap := PoolCap.Load()
	if cap <= 0 {
		return false
	}
	ibytes := int64(bytes)
	for {
		cur := elasticBorrowed.Load()
		if cur+ibytes > cap {
			return false
		}
		if elasticBorrowed.CompareAndSwap(cur, cur+ibytes) {
			if laneID >= 0 && laneID < len(laneBorrowed) {
				laneBorrowed[laneID].Add(bytes)
			}
			return true
		}
	}
}

// releaseElastic releases bytes of elastic headroom previously borrowed by laneID.
func releaseElastic(laneID int, bytes uint64) {
	if bytes == 0 {
		return
	}
	if laneID >= 0 && laneID < len(laneBorrowed) {
		for {
			cur := laneBorrowed[laneID].Load()
			toSub := min(bytes, cur)
			if laneBorrowed[laneID].CompareAndSwap(cur, cur-toSub) {
				break
			}
		}
	}
	ibytes := int64(bytes)
	for {
		cur := elasticBorrowed.Load()
		next := cur - ibytes
		if next < 0 {
			next = 0
		}
		if elasticBorrowed.CompareAndSwap(cur, next) {
			break
		}
	}
}

// resetElasticPool resets the elastic token pool at the start of a portfolio
// run: PoolCap = 72% - 70% of the limit (the shares' nominal total is always
// 70%), 0 without -elastic-mem.
func resetElasticPool(memLimit uint64) {
	clearElasticBorrowed()
	if !ElasticMem || memLimit == 0 {
		PoolCap.Store(0)
		return
	}
	if ElasticPoolPct > 0 { // exp-710: lend unused nominal budget; the 75% mapped backstop in acquireElastic still gates
		PoolCap.Store(int64(memLimit / 100 * uint64(ElasticPoolPct)))
		return
	}
	PoolCap.Store(max(int64(float64(memLimit)*0.72)-int64(memLimit/100*70), 0))
}

// clearElasticBorrowed returns every borrowed byte to the pool.
func clearElasticBorrowed() {
	elasticBorrowed.Store(0)
	for i := range laneBorrowed {
		laneBorrowed[i].Store(0)
	}
}
