// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package controllercheck

import (
	"slices"
	"time"

	"k8s.io/client-go/util/workqueue"
)

// Queue is a sequential, deterministic scheduling model usable by real event
// handlers. It models deduplication and deadlines, not worker concurrency, queue
// priority, or client-go's exact rate limiter. No goroutines or wall-clock sleeps.
// Call Get only when Len()>0; unlike a production queue, an empty Get panics.
type Queue[K comparable] struct {
	compare func(K, K) int
	Now     time.Duration
	due     map[K]time.Duration
	retries map[K]int
	stopped bool
}

func NewQueue[K comparable](compare func(K, K) int) *Queue[K] {
	return &Queue[K]{compare: compare, due: map[K]time.Duration{}, retries: map[K]int{}}
}

func (q *Queue[K]) Add(r K) { q.AddAfter(r, 0) }
func (q *Queue[K]) AddAfter(r K, delay time.Duration) {
	if q.stopped {
		return
	}
	due := q.Now + max(delay, 0)
	if old, ok := q.due[r]; !ok || due < old {
		q.due[r] = due
	}
}
func (q *Queue[K]) Ready() []K {
	var ready []K
	for r, due := range q.due {
		if due <= q.Now {
			ready = append(ready, r)
		}
	}
	slices.SortFunc(ready, q.compare)
	return ready
}
func (q *Queue[K]) Len() int { return len(q.Ready()) }
func (q *Queue[K]) Get() (K, bool) {
	if q.stopped {
		var zero K
		return zero, true
	}
	ready := q.Ready()
	if len(ready) == 0 {
		panic("controllercheck.Queue.Get requires a ready request")
	}
	q.Take(ready[0])
	return ready[0], false
}
func (q *Queue[K]) Take(r K)            { delete(q.due, r) }
func (q *Queue[K]) Done(K)              {}
func (q *Queue[K]) ShutDown()           { q.stopped = true }
func (q *Queue[K]) ShutDownWithDrain()  { q.ShutDown() }
func (q *Queue[K]) ShuttingDown() bool  { return q.stopped }
func (q *Queue[K]) Forget(r K)          { delete(q.retries, r) }
func (q *Queue[K]) NumRequeues(r K) int { return q.retries[r] }
func (q *Queue[K]) AddRateLimited(r K) {
	q.retries[r]++
	q.AddAfter(r, time.Second)
}

var _ workqueue.TypedRateLimitingInterface[string] = (*Queue[string])(nil)
