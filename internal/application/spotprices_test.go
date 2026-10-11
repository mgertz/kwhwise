package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mgertz/kwhwise/internal/domain"
)

type stubSource struct {
	calls  int
	region string
	day    time.Time
	prices []domain.SpotPrice
	err    error
}

func (s *stubSource) HourlyPrices(ctx context.Context, region string, day time.Time) ([]domain.SpotPrice, error) {
	s.calls++
	s.region = region
	s.day = day
	return s.prices, s.err
}

func TestCurrentUsesCacheAndRefreshesAtDanishMidnight(t *testing.T) {
	location := location(t)
	// Danish midnight is 22:00 UTC in October. TZ must not move this boundary.
	start := time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC)
	source := &stubSource{prices: dayPrices(start, 24)}
	service := NewSpotPrices(source, "DK2", location)
	for _, at := range []time.Time{start, start.Add(45 * time.Minute), start.Add(time.Hour), start.Add(23 * time.Hour)} {
		price, err := service.Current(context.Background(), at)
		if err != nil || at.Before(price.Start) || !at.Before(price.End) {
			t.Fatalf("invalid price at %s: %+v, error %v", at, price, err)
		}
	}
	if source.calls != 1 || source.region != "DK2" || !source.day.Equal(start) {
		t.Fatalf("unexpected source calls: %+v", source)
	}

	nextDay := start.Add(24 * time.Hour)
	source.prices = dayPrices(nextDay, 24)
	if _, err := service.Current(context.Background(), nextDay.In(time.FixedZone("Other TZ", -5*3600))); err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 {
		t.Fatalf("source calls = %d, want 2 after Danish midnight", source.calls)
	}
}

func TestCurrentKeepsRepeatedWinterHoursDistinct(t *testing.T) {
	start := time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC)
	source := &stubSource{prices: dayPrices(start, 25)}
	service := NewSpotPrices(source, "DK2", location(t))
	first, err := service.Current(context.Background(), start.Add(2*time.Hour+30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Current(context.Background(), start.Add(3*time.Hour+30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.Start.Equal(second.Start) || first.PriceOreExclVAT == second.PriceOreExclVAT || source.calls != 1 {
		t.Fatalf("repeated hours not distinguished: %+v and %+v, calls %d", first, second, source.calls)
	}
}

func TestCurrentDoesNotReusePreviousDayAfterFetchFailure(t *testing.T) {
	start := time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC)
	source := &stubSource{prices: dayPrices(start, 24)}
	service := NewSpotPrices(source, "DK2", location(t))
	if _, err := service.Current(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	networkError := errors.New("network unavailable")
	source.err = networkError
	if _, err := service.Current(context.Background(), start.Add(24*time.Hour)); !errors.Is(err, networkError) {
		t.Fatalf("error = %v, want fetch error instead of yesterday's price", err)
	}
	source.err = nil
	source.prices = dayPrices(start.Add(24*time.Hour), 24)
	if _, err := service.Current(context.Background(), start.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if source.calls != 3 {
		t.Fatalf("source calls = %d, want 3", source.calls)
	}
}

func TestCurrentDoesNotCacheResponseWithoutCurrentHour(t *testing.T) {
	start := time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC)
	source := &stubSource{prices: dayPrices(start.Add(time.Hour), 1)}
	service := NewSpotPrices(source, "DK2", location(t))
	if _, err := service.Current(context.Background(), start); err == nil {
		t.Fatal("expected error for missing current hour")
	}
	source.prices = dayPrices(start, 24)
	if _, err := service.Current(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 {
		t.Fatalf("invalid result was cached: source calls = %d", source.calls)
	}
}

func TestCurrentRespectsCancellationEvenWithCache(t *testing.T) {
	start := time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC)
	source := &stubSource{prices: dayPrices(start, 24)}
	service := NewSpotPrices(source, "DK2", location(t))
	if _, err := service.Current(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Current(ctx, start); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if source.calls != 1 {
		t.Fatal("cancelled call fetched prices")
	}
}

func dayPrices(start time.Time, hours int) []domain.SpotPrice {
	prices := make([]domain.SpotPrice, hours)
	for i := range prices {
		hour := start.Add(time.Duration(i) * time.Hour)
		prices[i] = domain.SpotPrice{Start: hour, End: hour.Add(time.Hour), PriceOreExclVAT: float64(i)}
	}
	return prices
}

func location(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		t.Fatal(err)
	}
	return location
}
