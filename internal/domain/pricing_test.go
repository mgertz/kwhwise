package domain_test

import (
	"math"
	"testing"
	"time"

	"github.com/mgertz/kwhwise/internal/adapters/profiledata"
	"github.com/mgertz/kwhwise/internal/domain"
	"github.com/mgertz/kwhwise/profiles"
)

func TestCeriusAndNationalChargesMatchVerifiedGridTotals(t *testing.T) {
	rules, err := profiledata.Load(profiles.Files, "nrgi", "cerius", "DK2")
	if err != nil {
		t.Fatal(err)
	}
	calculator, err := domain.NewCalculator(rules, 9, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		hour          int
		wantGridTotal float64
	}{
		{0, 29.792}, {6, 58.626}, {16, 58.626}, {17, 145.128125}, {20, 145.128125}, {21, 58.626},
	} {
		start := time.Date(2026, 10, 9, test.hour, 0, 0, 0, rules.Location).UTC()
		result, err := calculator.Calculate(domain.SpotPrice{Start: start, End: start.Add(time.Hour), PriceOreExclVAT: 80})
		if err != nil {
			t.Fatal(err)
		}
		grid := result.Components["grid_tariff"].InclVAT + result.Components["transmission_tariff"].InclVAT +
			result.Components["system_tariff"].InclVAT + result.Components["electricity_tax"].InclVAT
		assertClose(t, grid, test.wantGridTotal)
		assertClose(t, result.Components["spot_price_markup"].InclVAT, 9)
		assertClose(t, result.TotalInclVAT, 100+test.wantGridTotal+9)
		assertClose(t, result.TotalExclVAT+result.TotalVAT, result.TotalInclVAT)
	}
}

func TestMarkupVATAndNegativeSpotPrices(t *testing.T) {
	rules, err := profiledata.Load(profiles.Files, "nrgi", "cerius", "DK2")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 9, 0, 0, 0, 0, rules.Location).UTC()
	for _, test := range []struct {
		vat   domain.VATMode
		gross float64
		tax   float64
	}{
		{domain.VATIncluded, 9, 1.8}, {domain.VATExcluded, 11.25, 2.25}, {domain.VATNone, 9, 0},
	} {
		calculator, err := domain.NewCalculator(rules, 9, test.vat)
		if err != nil {
			t.Fatal(err)
		}
		result, err := calculator.Calculate(domain.SpotPrice{Start: start, End: start.Add(time.Hour), PriceOreExclVAT: -100})
		if err != nil {
			t.Fatal(err)
		}
		assertClose(t, result.Components["spot_price_markup"].InclVAT, test.gross)
		assertClose(t, result.Components["spot_price_markup"].VAT, test.tax)
		assertClose(t, result.Components["spot_price"].InclVAT, -125)
		assertClose(t, result.TotalInclVAT, -125+29.792+test.gross)
	}
}

func TestKnownAccountTotalWithExplicitMarkup(t *testing.T) {
	rules, err := profiledata.Load(profiles.Files, "nrgi", "cerius", "DK2")
	if err != nil {
		t.Fatal(err)
	}
	calculator, err := domain.NewCalculator(rules, 11.0125, domain.VATIncluded)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 9, 0, 0, 0, 0, rules.Location).UTC()
	result, err := calculator.Calculate(domain.SpotPrice{Start: start, End: start.Add(time.Hour), PriceOreExclVAT: 87.4633265625 / 1.25})
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, result.TotalInclVAT, 128.2678265625)
}

func TestSeasonBoundaryAndExpiredRates(t *testing.T) {
	rules, err := profiledata.Load(profiles.Files, "nrgi", "cerius", "DK2")
	if err != nil {
		t.Fatal(err)
	}
	calculator, err := domain.NewCalculator(rules, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		date string
		grid float64
	}{
		{"2026-09-30T17:00:00+02:00", 44.9811}, {"2026-10-01T17:00:00+02:00", 103.8025},
	} {
		start, err := time.Parse(time.RFC3339, test.date)
		if err != nil {
			t.Fatal(err)
		}
		result, err := calculator.Calculate(domain.SpotPrice{Start: start, End: start.Add(time.Hour), PriceOreExclVAT: 0})
		if err != nil {
			t.Fatal(err)
		}
		assertClose(t, result.Components["grid_tariff"].ExclVAT, test.grid)
	}
	for _, date := range []string{"2026-03-31T12:00:00+02:00", "2027-01-01T00:00:00+01:00", "2027-04-01T12:00:00+02:00"} {
		start, _ := time.Parse(time.RFC3339, date)
		if _, err := calculator.Calculate(domain.SpotPrice{Start: start, End: start.Add(time.Hour)}); err == nil {
			t.Errorf("accepted price outside verified coverage at %s", date)
		}
	}
}

func TestWinterTimeAndWeekendUseSameLocalGridPlan(t *testing.T) {
	rules, err := profiledata.Load(profiles.Files, "nrgi", "cerius", "DK2")
	if err != nil {
		t.Fatal(err)
	}
	calculator, err := domain.NewCalculator(rules, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2026-10-25T02:00:00+02:00", "2026-10-25T02:00:00+01:00", "2026-10-24T02:00:00+02:00"} {
		start, _ := time.Parse(time.RFC3339, date)
		result, err := calculator.Calculate(domain.SpotPrice{Start: start, End: start.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		assertClose(t, result.Components["grid_tariff"].InclVAT, 14.417)
	}
}

func assertClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %.12f, want %.12f", got, want)
	}
}
