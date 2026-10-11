package application

import (
	"context"
	"testing"
	"time"

	"github.com/mgertz/kwhwise/internal/adapters/profiledata"
	"github.com/mgertz/kwhwise/internal/domain"
	"github.com/mgertz/kwhwise/profiles"
)

func TestPricesCalculatesCurrentPriceAndWholeDayFromCachedSpotData(t *testing.T) {
	rules, err := profiledata.Load(profiles.Files, "nrgi", "cerius", "DK2")
	if err != nil {
		t.Fatal(err)
	}
	calculator, err := domain.NewCalculator(rules, 9, "")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 9, 0, 0, 0, 0, rules.Location).UTC()
	source := &stubSource{prices: dayPrices(start, 24)}
	spot := NewSpotPrices(source, "DK2", rules.Location)
	service := NewPrices(spot, calculator)
	for _, hour := range []int{0, 6, 17, 23} {
		at := start.Add(time.Duration(hour)*time.Hour + 30*time.Minute)
		snapshot, err := service.At(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Forecast) != 24 || !snapshot.Current.Start.Equal(start.Add(time.Duration(hour)*time.Hour)) {
			t.Fatalf("invalid current price/forecast: %+v", snapshot)
		}
	}
	if source.calls != 1 {
		t.Fatalf("source calls = %d, want 1", source.calls)
	}
	// Day's return value is a copy, not a mutable reference to the spot cache.
	prices, err := spot.Day(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	prices[0].PriceOreExclVAT = 999
	if current, _ := spot.Current(context.Background(), start); current.PriceOreExclVAT == 999 {
		t.Fatal("caller mutated cache")
	}
}
