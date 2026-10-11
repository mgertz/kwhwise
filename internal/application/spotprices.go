package application

import (
	"context"
	"fmt"
	"time"

	"github.com/mgertz/kwhwise/internal/domain"
)

// SpotPriceSource is the port implemented by a spot-price adapter. The
// application does not know about HTTP, JSON or a particular data provider.
type SpotPriceSource interface {
	HourlyPrices(ctx context.Context, region string, day time.Time) ([]domain.SpotPrice, error)
}

// SpotPrices caches one Danish day's prices. The scheduler calls it
// sequentially; no worker or additional timer is needed for the cache.
type SpotPrices struct {
	source    SpotPriceSource
	region    string
	location  *time.Location
	cachedDay string
	prices    []domain.SpotPrice
}

func NewSpotPrices(source SpotPriceSource, region string, location *time.Location) *SpotPrices {
	return &SpotPrices{source: source, region: region, location: location}
}

func (s *SpotPrices) Current(ctx context.Context, at time.Time) (domain.SpotPrice, error) {
	if err := ctx.Err(); err != nil {
		return domain.SpotPrice{}, err
	}
	day := at.In(s.location).Format(time.DateOnly)
	prices := s.prices
	if day != s.cachedDay {
		var err error
		prices, err = s.source.HourlyPrices(ctx, s.region, at)
		if err != nil {
			return domain.SpotPrice{}, fmt.Errorf("load spot prices for %s: %w", day, err)
		}
	}
	for _, price := range prices {
		if !at.Before(price.Start) && at.Before(price.End) {
			// Only replace the cache after a successful lookup. A failed fetch or
			// response without the requested hour must not poison the cache.
			s.cachedDay = day
			s.prices = prices
			return price, nil
		}
	}
	return domain.SpotPrice{}, fmt.Errorf("no valid spot price at %s", at.Format(time.RFC3339))
}

// Day returns the validated day after ensuring it contains the current hour.
// The copy prevents callers from mutating the cache.
func (s *SpotPrices) Day(ctx context.Context, at time.Time) ([]domain.SpotPrice, error) {
	if _, err := s.Current(ctx, at); err != nil {
		return nil, err
	}
	return append([]domain.SpotPrice(nil), s.prices...), nil
}
