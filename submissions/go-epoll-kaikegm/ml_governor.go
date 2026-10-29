package main

import (
	"sync/atomic"
	"time"
)

// MLGovernor implements a zero-allocation, fixed-point Adaptive Governor
// for twelve-dollar-server-challenge. It dynamically optimizes:
// 1. Proactive LRU connection eviction to avoid accept queue backpressure and 60s timeouts at 180k+ users.
// 2. Non-blocking Epoll opportunistic coalescing to maximize SQLite C-Engine batch density.
type MLGovernor struct {
	// Running EWMA statistics
	avgBatchSize     atomic.Int64
	avgBatchDuration atomic.Int64
	totalInferences  atomic.Uint64
	totalCoalesced   atomic.Uint64
	wastedDrains     atomic.Uint64

	// Q-Table for opportunistic extra drain decisions:
	// [batchBucket: 4][connPressure: 2][sqliteDuration: 2][action: 2]
	// Action 0: Don't drain extra, execute SQLite immediately.
	// Action 1: Opportunistic non-blocking Epoll drain (timeout 0).
	// Total elements: 4 * 2 * 2 * 2 = 32 int32 = 128 bytes. Fits instantly in L1 cache.
	drainQ [4][2][2][2]int32
}

func NewMLGovernor() *MLGovernor {
	g := &MLGovernor{}
	g.avgBatchSize.Store(16)
	g.avgBatchDuration.Store(150) // default 150us SQLite batch time

	// Analytical priors:
	// If batch is very small (< 4), Action 1 (non-blocking drain) has higher reward under low connection pressure.
	// If batch is large (>= 16) or SQLite is slow, Action 0 (immediate execution) is strongly preferred.
	for bIdx := 0; bIdx < 4; bIdx++ {
		for cIdx := 0; cIdx < 2; cIdx++ {
			for dIdx := 0; dIdx < 2; dIdx++ {
				if bIdx == 0 && cIdx == 0 {
					g.drainQ[bIdx][cIdx][dIdx][0] = 12000
					g.drainQ[bIdx][cIdx][dIdx][1] = 22000 // Small batch + low pressure: coalescing preferred
				} else if bIdx == 1 && cIdx == 0 {
					g.drainQ[bIdx][cIdx][dIdx][0] = 16000
					g.drainQ[bIdx][cIdx][dIdx][1] = 20000
				} else if bIdx >= 2 || cIdx == 1 || dIdx == 1 {
					g.drainQ[bIdx][cIdx][dIdx][0] = 26000 // High pressure or large batch: immediate execution
					g.drainQ[bIdx][cIdx][dIdx][1] = 14000
				}
			}
		}
	}
	return g
}

// PredictGraceMs calculates the adaptive idle eviction grace period in milliseconds.
// Under normal load, it keeps connections warm for 1500ms.
// As openConns approaches softMaxFDs and hardMaxFDs, it smoothly tightens graceMs
// to proactively shed idle FDs before acceptAll() is forced to pause the listener.
func (g *MLGovernor) PredictGraceMs(openConns, softMax, hardMax int) int64 {
	g.totalInferences.Add(1)

	if openConns <= softMax {
		return idleGraceMs // 1500ms baseline
	}

	// Under FD pressure: compute linear decay based on remaining headroom
	headroom := hardMax - openConns
	rangeFDs := hardMax - softMax
	if rangeFDs <= 0 {
		rangeFDs = 1
	}

	if headroom <= 0 {
		return 50 // Minimum grace 50ms under emergency hard cap
	}

	decayGrace := (int64(headroom) * idleGraceMs) / int64(rangeFDs)
	if decayGrace < 100 {
		decayGrace = 100
	} else if decayGrace > idleGraceMs {
		decayGrace = idleGraceMs
	}
	return decayGrace
}

// ShouldDrainExtra predicts whether an opportunistic non-blocking epoll_wait(..., 0)
// will yield higher SQLite batch throughput without introducing latency overhead.
func (g *MLGovernor) ShouldDrainExtra(batchSize, openConns, softMax int) bool {
	// Safety Cap: if batch is already large (>= 16), execute immediately without extra syscall.
	if batchSize >= 16 {
		return false
	}

	var bIdx int
	if batchSize < 4 {
		bIdx = 0
	} else if batchSize < 8 {
		bIdx = 1
	} else {
		bIdx = 2
	}

	cIdx := 0
	if openConns >= softMax {
		cIdx = 1
	}

	dIdx := 0
	if g.avgBatchDuration.Load() > 200 {
		dIdx = 1
	}

	return g.drainQ[bIdx][cIdx][dIdx][1] > g.drainQ[bIdx][cIdx][dIdx][0]
}

// Update records batch completion metrics and updates the Q-values and EWMAs.
func (g *MLGovernor) Update(batchSize, openConns, softMax int, duration time.Duration, didDrainExtra, gotExtra bool) {
	us := duration.Microseconds()
	if us <= 0 {
		us = 1
	}

	// Update EWMA of batch metrics
	oldDur := g.avgBatchDuration.Load()
	g.avgBatchDuration.Store(oldDur + ((us - oldDur) >> 3))

	oldSize := g.avgBatchSize.Load()
	g.avgBatchSize.Store(oldSize + ((int64(batchSize) - oldSize) >> 3))

	if gotExtra {
		g.totalCoalesced.Add(1)
	} else if didDrainExtra {
		g.wastedDrains.Add(1)
	}

	// Effective throughput of this batch in operations per second
	reward := int32((int64(batchSize) * 1000000) / us)
	if didDrainExtra && !gotExtra {
		// Penalty for wasted non-blocking drain syscall that yielded 0 events
		reward = reward / 2
	}

	var bIdx int
	if batchSize < 4 {
		bIdx = 0
	} else if batchSize < 8 {
		bIdx = 1
	} else if batchSize < 16 {
		bIdx = 2
	} else {
		bIdx = 3
	}

	cIdx := 0
	if openConns >= softMax {
		cIdx = 1
	}

	dIdx := 0
	if oldDur > 200 {
		dIdx = 1
	}

	action := 0
	if didDrainExtra {
		action = 1
	}

	oldQ := g.drainQ[bIdx][cIdx][dIdx][action]
	g.drainQ[bIdx][cIdx][dIdx][action] = oldQ + ((reward - oldQ) >> 3)
}
