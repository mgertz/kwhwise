package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNextHour(t *testing.T) {
	tests := []struct {
		name string
		zone string
		now  string
		want string
	}{
		{"normal hour", "Europe/Copenhagen", "2026-01-15T12:34:56.123+01:00", "2026-01-15T13:00:00+01:00"},
		{"exact hour", "Europe/Copenhagen", "2026-01-15T12:00:00+01:00", "2026-01-15T13:00:00+01:00"},
		{"midnight", "Europe/Copenhagen", "2026-01-15T23:59:59+01:00", "2026-01-16T00:00:00+01:00"},
		{"summer time starts", "Europe/Copenhagen", "2026-03-29T01:30:00+01:00", "2026-03-29T03:00:00+02:00"},
		{"winter time first occurrence", "Europe/Copenhagen", "2026-10-25T02:30:00+02:00", "2026-10-25T02:00:00+01:00"},
		{"winter time second occurrence", "Europe/Copenhagen", "2026-10-25T02:30:00+01:00", "2026-10-25T03:00:00+01:00"},
		{"UTC input", "Europe/Copenhagen", "2026-01-15T11:34:56Z", "2026-01-15T13:00:00+01:00"},
		{"half hour offset", "Asia/Kolkata", "2026-01-15T12:34:56+05:30", "2026-01-15T13:00:00+05:30"},
		{"half hour DST starts", "Australia/Lord_Howe", "2026-10-04T01:45:00+10:30", "2026-10-04T03:00:00+11:00"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			location, err := time.LoadLocation(test.zone)
			if err != nil {
				t.Fatal(err)
			}
			now := parseTime(t, test.now)
			want := parseTime(t, test.want)
			got := NextHour(now, location)
			if !got.Equal(want) || got.Location() != location {
				t.Errorf("NextHour() = %s (%s), want %s (%s)", got, got.Location(), want, location)
			}
			if !got.After(now) {
				t.Errorf("NextHour() must be strictly after %s, got %s", now, got)
			}
		})
	}
}

func TestRetryBackoffResetsAfterSuccessAndReturnsToHourlySchedule(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := parseTime(t, "2026-10-09T12:34:00Z")
	calls := 0
	waits := 0
	want := []time.Time{
		current.Add(5 * time.Second), current.Add(15 * time.Second), parseTime(t, "2026-10-09T13:00:00Z"), parseTime(t, "2026-10-09T13:00:05Z"),
	}
	runWithRetry(ctx, time.UTC, func(context.Context, time.Time) error {
		calls++
		if calls == 3 {
			return nil
		}
		return errors.New("temporary failure")
	}, func() time.Time { return current }, func(ctx context.Context, deadline time.Time) bool {
		if !deadline.Equal(want[waits]) {
			t.Errorf("deadline = %s, want %s", deadline, want[waits])
		}
		current = deadline
		waits++
		return waits < len(want)
	})
}

type retryAtError struct{ until time.Time }

func (e retryAtError) Error() string      { return "rate limited" }
func (e retryAtError) RetryAt() time.Time { return e.until }

func TestRetryRespectsServerDeadline(t *testing.T) {
	current := parseTime(t, "2026-10-09T12:34:00Z")
	until := current.Add(2 * time.Hour)
	runWithRetry(context.Background(), time.UTC, func(context.Context, time.Time) error { return retryAtError{until} },
		func() time.Time { return current }, func(ctx context.Context, deadline time.Time) bool {
			if !deadline.Equal(until) {
				t.Errorf("deadline = %s, want %s", deadline, until)
			}
			return false
		})
}

func TestRunUpdatesImmediatelyAndAlignsHoursAfterWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := parseTime(t, "2026-01-15T12:34:56Z")
	updates := 0
	waits := 0
	run(ctx, time.UTC, func(receivedContext context.Context, triggeredAt time.Time) {
		if receivedContext != ctx || !triggeredAt.Equal(current) {
			t.Fatalf("unexpected callback context or time: %s", triggeredAt)
		}
		updates++
		// Simulate two seconds of work. The next deadline must not drift by it.
		current = current.Add(2 * time.Second)
		if updates == 3 {
			cancel()
		}
	}, func() time.Time {
		return current
	}, func(ctx context.Context, deadline time.Time) bool {
		waits++
		want := parseTime(t, "2026-01-15T13:00:00Z").Add(time.Duration(waits-1) * time.Hour)
		if !deadline.Equal(want) {
			t.Fatalf("deadline = %s, want %s", deadline, want)
		}
		current = deadline
		return true
	})
	if updates != 3 || waits != 2 {
		t.Fatalf("updates = %d, waits = %d, want 3 and 2", updates, waits)
	}
}

func TestRunDoesNotUpdateWithCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Run(ctx, time.UTC, func(context.Context, time.Time) {
		t.Fatal("update invoked after cancellation")
	})
}

func TestRunStopsWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, time.UTC, func(context.Context, time.Time) {}, time.Now, func(ctx context.Context, deadline time.Time) bool {
			close(started)
			return waitUntil(ctx, time.Now().Add(time.Hour))
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not enter wait")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
}

func TestRunWaitsForFutureHourAfterLongUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := parseTime(t, "2026-01-15T12:34:56Z")
	run(ctx, time.UTC, func(context.Context, time.Time) {
		current = current.Add(2 * time.Hour)
	}, func() time.Time {
		return current
	}, func(ctx context.Context, deadline time.Time) bool {
		want := parseTime(t, "2026-01-15T15:00:00Z")
		if !deadline.Equal(want) {
			t.Fatalf("deadline = %s, want %s", deadline, want)
		}
		return false
	})
}

func parseTime(t *testing.T, value string) time.Time {
	t.Helper()
	result, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
