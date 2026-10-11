package application

import (
	"context"
	"fmt"
	"time"

	"github.com/mgertz/kwhwise/internal/domain"
)

type PriceCalculator interface {
	Calculate(domain.SpotPrice) (domain.PriceBreakdown, error)
}

type Snapshot struct {
	Current  domain.PriceBreakdown
	Forecast []domain.PriceBreakdown
}

type PricePublisher interface {
	Publish(context.Context, Snapshot) error
	Unavailable(context.Context) error
}

type Prices struct {
	spot       *SpotPrices
	calculator PriceCalculator
}

func NewPrices(spot *SpotPrices, calculator PriceCalculator) *Prices {
	return &Prices{spot: spot, calculator: calculator}
}

func (p *Prices) At(ctx context.Context, at time.Time) (Snapshot, error) {
	spotPrices, err := p.spot.Day(ctx, at)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{Forecast: make([]domain.PriceBreakdown, 0, len(spotPrices))}
	found := false
	for _, spot := range spotPrices {
		price, err := p.calculator.Calculate(spot)
		if err != nil {
			return Snapshot{}, fmt.Errorf("calculate price: %w", err)
		}
		result.Forecast = append(result.Forecast, price)
		if !at.Before(price.Start) && at.Before(price.End) {
			result.Current, found = price, true
		}
	}
	if !found {
		return Snapshot{}, fmt.Errorf("no current calculated price at %s", at.Format(time.RFC3339))
	}
	return result, nil
}
