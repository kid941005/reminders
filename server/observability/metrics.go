// Package observability provides process-wide counters and start time used by
// the health endpoint and structured logs. It deliberately avoids external
// dependencies so any layer can import it without cycles.
package observability

import (
	"sync/atomic"
	"time"
)

// StartTime is the process start instant (set at package init).
var StartTime = time.Now()

// Counter is a monotonically increasing int64 safe for concurrent use.
type Counter struct{ v atomic.Int64 }

func (c *Counter) Inc()         { c.v.Add(1) }
func (c *Counter) Add(n int64)  { c.v.Add(n) }
func (c *Counter) Value() int64 { return c.v.Load() }

var (
	HTTPRequests      Counter
	HTTPErrors        Counter
	DeliverySucceeded Counter
	DeliveryFailed    Counter
	DeliveryRetried   Counter
)

// Snapshot returns all counters for /api/health.
func Snapshot() map[string]int64 {
	return map[string]int64{
		"http_requests":      HTTPRequests.Value(),
		"http_errors":        HTTPErrors.Value(),
		"delivery_succeeded": DeliverySucceeded.Value(),
		"delivery_failed":    DeliveryFailed.Value(),
		"delivery_retried":   DeliveryRetried.Value(),
	}
}

// UptimeSeconds reports process uptime.
func UptimeSeconds() int64 { return int64(time.Since(StartTime).Seconds()) }
