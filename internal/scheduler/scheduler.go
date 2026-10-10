package scheduler

import (
	"context"
	"errors"
	"time"
)

// Run invokes update immediately, then at each next local full hour. Updates
// never overlap. The callback must respect ctx when doing blocking work.
func Run(ctx context.Context, location *time.Location, update func(context.Context, time.Time)) {
	run(ctx, location, update, time.Now, waitUntil)
}

// RunWithRetry aligns successful updates to the hour and retries failures
// using bounded backoff. A source's RetryAt deadline takes precedence.
func RunWithRetry(ctx context.Context, location *time.Location, update func(context.Context, time.Time) error) {
	runWithRetry(ctx, location, update, time.Now, waitUntil)
}

// The clock and wait functions allow scheduling to be tested without real
// hour-long sleeps or a separate clock interface.
func run(
	ctx context.Context,
	location *time.Location,
	update func(context.Context, time.Time),
	now func() time.Time,
	wait func(context.Context, time.Time) bool,
) {
	runWithRetry(ctx, location, func(ctx context.Context, at time.Time) error { update(ctx, at); return nil }, now, wait)
}

func runWithRetry(
	ctx context.Context,
	location *time.Location,
	update func(context.Context, time.Time) error,
	now func() time.Time,
	wait func(context.Context, time.Time) bool,
) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		err := update(ctx, now().In(location))
		if ctx.Err() != nil {
			return
		}
		// Recalculate after the work to avoid drift and catch-up bursts.
		current := now()
		deadline := NextHour(current, location)
		if err != nil {
			deadline = current.Add(backoff)
			if hour := NextHour(current, location); hour.Before(deadline) {
				deadline = hour
			}
			var limited interface{ RetryAt() time.Time }
			if errors.As(err, &limited) && limited.RetryAt().After(deadline) {
				deadline = limited.RetryAt()
			}
			backoff = min(backoff*2, 5*time.Minute)
		} else {
			backoff = 5 * time.Second
		}
		if !wait(ctx, deadline) {
			return
		}
	}
}

func waitUntil(ctx context.Context, deadline time.Time) bool {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// NextHour returns the next local full hour in the supplied location.
func NextHour(now time.Time, location *time.Location) time.Time {
	// Advance along the actual timeline instead of constructing an ambiguous
	// local time. This preserves the repeated hour when daylight saving ends,
	// and also supports zones with non-hour offsets or half-hour DST changes.
	next := nextMinute(now.In(location))
	for next.Minute() != 0 || next.Second() != 0 {
		next = nextMinute(next)
	}
	return next
}

func nextMinute(now time.Time) time.Time {
	return now.Add(time.Minute - time.Duration(now.Second())*time.Second - time.Duration(now.Nanosecond()))
}
