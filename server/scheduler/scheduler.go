// Package scheduler is a tiny registry for background jobs, mirroring the apps
// registry. An app registers a periodic job once and the framework runs it for
// the process lifetime, recovering from panics so one bad job can't crash the
// server.
//
//	func init() {
//	    scheduler.Register(scheduler.Job{
//	        Name:     "cleanup",
//	        Interval: time.Hour,
//	        Run:      func() { /* ... */ },
//	    })
//	    // or a fixed daily time:
//	    scheduler.Register(scheduler.Job{Name: "digest", Daily: "08:00", Run: sendDigest})
//	}
package scheduler

import (
	"fmt"
	"sync"
	"time"

	"smallgo/server/logger"
)

type Job struct {
	Name string
	// Interval runs Run every Interval. Takes precedence over Daily.
	Interval time.Duration
	// Daily runs Run once per day at this local "15:04" time (used when
	// Interval is 0).
	Daily string
	// RunAtStart runs Run once immediately when the scheduler starts.
	RunAtStart bool
	Run        func()
}

var registered []Job

// JobStatus is the observed runtime state of one job, exposed by /api/health
// and the admin diagnostics so a silently failing job becomes visible.
type JobStatus struct {
	Name         string    `json:"name"`
	Interval     string    `json:"interval,omitempty"`
	Daily        string    `json:"daily,omitempty"`
	Runs         int64     `json:"runs"`
	LastRun      time.Time `json:"last_run"`
	LastDuration string    `json:"last_duration"`
	LastError    string    `json:"last_error,omitempty"`
}

var (
	statusMu sync.RWMutex
	statuses = map[string]*JobStatus{}
)

// Snapshot returns one entry per job that has run at least once (plus jobs
// registered with RunAtStart as soon as they start).
func Snapshot() []JobStatus {
	statusMu.RLock()
	defer statusMu.RUnlock()
	out := make([]JobStatus, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, *s)
	}
	return out
}

func recordJob(name string, d time.Duration, errMsg string) {
	statusMu.Lock()
	defer statusMu.Unlock()
	s := statuses[name]
	if s == nil {
		s = &JobStatus{Name: name}
		statuses[name] = s
	}
	s.Runs++
	s.LastRun = time.Now().UTC()
	s.LastDuration = d.String()
	s.LastError = errMsg
}

// Register adds a job. Call from init() before the server starts.
func Register(j Job) {
	registered = append(registered, j)
	statusMu.Lock()
	if _, ok := statuses[j.Name]; !ok {
		statuses[j.Name] = &JobStatus{Name: j.Name, Interval: j.Interval.String(), Daily: j.Daily}
	}
	statusMu.Unlock()
}

// All returns the registered jobs (useful for tests/introspection).
func All() []Job { return registered }

type Scheduler struct{ stop chan struct{} }

// Start launches every registered job in its own goroutine.
func Start() *Scheduler {
	s := &Scheduler{stop: make(chan struct{})}
	for _, j := range registered {
		go s.run(j)
	}
	if len(registered) > 0 {
		logger.Info("scheduler: started %d job(s)", len(registered))
	}
	return s
}

// Stop signals all jobs to exit. Running jobs finish their current iteration.
func (s *Scheduler) Stop() { close(s.stop) }

func (s *Scheduler) run(j Job) {
	safeRun := func() {
		start := time.Now()
		var errMsg string
		defer func() {
			if r := recover(); r != nil {
				errMsg = "panic: " + fmt.Sprint(r)
				logger.Error("scheduler: job %q panicked: %v", j.Name, r)
			}
			recordJob(j.Name, time.Since(start), errMsg)
		}()
		j.Run()
	}

	if j.RunAtStart {
		safeRun()
	}

	switch {
	case j.Interval > 0:
		t := time.NewTicker(j.Interval)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				safeRun()
			}
		}
	case j.Daily != "":
		for {
			timer := time.NewTimer(untilNext(j.Daily))
			select {
			case <-s.stop:
				timer.Stop()
				return
			case <-timer.C:
				safeRun()
			}
		}
	default:
		logger.Warn("scheduler: job %q has no Interval or Daily; not scheduled", j.Name)
	}
}

// scheduleLocation is the wall clock used by Daily jobs. The product contract
// is Asia/Shanghai regardless of the host timezone (containers usually run in
// UTC), so a fixed-offset fallback keeps scheduling correct even without tzdata.
func scheduleLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*60*60)
}

// untilNext returns the duration from now until the next occurrence of the
// "15:04" Asia/Shanghai wall-clock time hhmm. Falls back to one hour on a
// parse error.
func untilNext(hhmm string) time.Duration {
	loc := scheduleLocation()
	now := time.Now().In(loc)
	t, err := time.ParseInLocation("15:04", hhmm, loc)
	if err != nil {
		logger.Warn("scheduler: invalid Daily time %q, retrying in 1h", hhmm)
		return time.Hour
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}
