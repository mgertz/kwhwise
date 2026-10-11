package domain

import (
	"fmt"
	"math"
	"time"
)

type VATMode string

const (
	VATIncluded VATMode = "included"
	VATExcluded VATMode = "excluded"
	VATNone     VATMode = "none"
)

func (m VATMode) Valid() bool { return m == VATIncluded || m == VATExcluded || m == VATNone }

type Window struct {
	From  time.Time
	Until time.Time
}

func (w Window) Contains(at time.Time) bool { return !at.Before(w.From) && at.Before(w.Until) }

type Season struct {
	From  string
	Until string
	Rates [24]float64
}

func (s Season) Contains(monthDay string) bool {
	if s.From < s.Until {
		return monthDay >= s.From && monthDay < s.Until
	}
	return monthDay >= s.From || monthDay < s.Until
}

type GridPeriod struct {
	Window
	Seasons []Season
}

type ChargePeriod struct {
	Window
	Value float64
}

type ChargeProfile struct {
	ID      string
	VAT     VATMode
	Periods []ChargePeriod
}

type PricingRules struct {
	Region           string
	Provider         string
	GridOperator     string
	CustomerGroup    string
	Location         *time.Location
	DefaultMarkupVAT VATMode
	VATRate          float64
	GridVAT          VATMode
	GridPeriods      []GridPeriod
	NationalCharges  []ChargeProfile
}

// All component amounts and totals are in ore/kWh. Included and excluded
// source prices are normalized once; VAT is never added twice.
type Component struct {
	ExclVAT float64 `json:"excl_vat_ore"`
	VAT     float64 `json:"vat_ore"`
	InclVAT float64 `json:"incl_vat_ore"`
}

type PriceBreakdown struct {
	Start        time.Time
	End          time.Time
	Components   map[string]Component
	TotalExclVAT float64
	TotalVAT     float64
	TotalInclVAT float64
}

type Calculator struct {
	rules     PricingRules
	markup    float64
	markupVAT VATMode
}

func NewCalculator(rules PricingRules, markup float64, markupVAT VATMode) (*Calculator, error) {
	if markupVAT == "" {
		markupVAT = rules.DefaultMarkupVAT
	}
	if !markupVAT.Valid() || !finite(markup) || markup < 0 || rules.Location == nil ||
		!finite(rules.VATRate) || rules.VATRate < 0 || !rules.GridVAT.Valid() {
		return nil, fmt.Errorf("invalid pricing configuration")
	}
	return &Calculator{rules: rules, markup: markup, markupVAT: markupVAT}, nil
}

func (c *Calculator) Calculate(spot SpotPrice) (PriceBreakdown, error) {
	if !finite(spot.PriceOreExclVAT) || !spot.End.Equal(spot.Start.Add(time.Hour)) {
		return PriceBreakdown{}, fmt.Errorf("invalid hourly spot price")
	}
	local := spot.Start.In(c.rules.Location)
	var grid *Season
	for _, period := range c.rules.GridPeriods {
		if period.Contains(local) {
			for i := range period.Seasons {
				if period.Seasons[i].Contains(local.Format("01-02")) {
					grid = &period.Seasons[i]
				}
			}
		}
	}
	if grid == nil {
		return PriceBreakdown{}, fmt.Errorf("no valid %s grid tariff for %s", c.rules.GridOperator, local.Format(time.DateOnly))
	}
	result := PriceBreakdown{Start: spot.Start, End: spot.End, Components: make(map[string]Component, 6)}
	add := func(id string, amount float64, mode VATMode) {
		component := component(amount, mode, c.rules.VATRate)
		result.Components[id] = component
		result.TotalExclVAT += component.ExclVAT
		result.TotalVAT += component.VAT
		result.TotalInclVAT += component.InclVAT
	}
	add("spot_price", spot.PriceOreExclVAT, VATExcluded)
	add("spot_price_markup", c.markup, c.markupVAT)
	add("grid_tariff", grid.Rates[local.Hour()], c.rules.GridVAT)
	for _, charge := range c.rules.NationalCharges {
		found := false
		for _, period := range charge.Periods {
			if period.Contains(local) {
				add(charge.ID, period.Value, charge.VAT)
				found = true
				break
			}
		}
		if !found {
			return PriceBreakdown{}, fmt.Errorf("no valid %s rate for %s", charge.ID, local.Format(time.DateOnly))
		}
	}
	if !finite(result.TotalInclVAT) {
		return PriceBreakdown{}, fmt.Errorf("invalid calculated total")
	}
	return result, nil
}

func component(amount float64, mode VATMode, rate float64) Component {
	result := Component{ExclVAT: amount, InclVAT: amount}
	switch mode {
	case VATExcluded:
		result.VAT = amount * rate
		result.InclVAT = amount + result.VAT
	case VATIncluded:
		result.ExclVAT = amount / (1 + rate)
		result.VAT = amount - result.ExclVAT
	}
	return result
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
