package profiledata

import (
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mgertz/kwhwise/internal/domain"
	"go.yaml.in/yaml/v3"
)

var identifier = regexp.MustCompile(`^[a-z0-9_-]+$`)

type providerFile struct {
	Version    int            `yaml:"schema_version"`
	ID         string         `yaml:"id"`
	Name       string         `yaml:"name"`
	Regions    []string       `yaml:"regions"`
	Unit       string         `yaml:"unit"`
	MarkupVAT  domain.VATMode `yaml:"default_markup_vat"`
	Source     string         `yaml:"source"`
	VerifiedAt string         `yaml:"verified_at"`
}

type gridFile struct {
	Version       int            `yaml:"schema_version"`
	Operator      string         `yaml:"operator"`
	CustomerGroup string         `yaml:"customer_group"`
	Regions       []string       `yaml:"regions"`
	Timezone      string         `yaml:"timezone"`
	Unit          string         `yaml:"unit"`
	VAT           domain.VATMode `yaml:"vat"`
	Periods       []gridPeriod   `yaml:"periods"`
}

type metadata struct {
	From       string `yaml:"valid_from"`
	Until      string `yaml:"valid_until"`
	Source     string `yaml:"source"`
	VerifiedAt string `yaml:"verified_at"`
}

type gridPeriod struct {
	metadata `yaml:",inline"`
	Seasons  map[string]season `yaml:"seasons"`
}

type season struct {
	From  string `yaml:"from"`
	Until string `yaml:"until"`
	Rates []rate `yaml:"rates"`
}

type rate struct {
	From  string   `yaml:"from"`
	Until string   `yaml:"to"`
	Value *float64 `yaml:"value"`
}

type nationalFile struct {
	Version  int      `yaml:"schema_version"`
	Country  string   `yaml:"country"`
	Timezone string   `yaml:"timezone"`
	Unit     string   `yaml:"unit"`
	VATRate  *float64 `yaml:"vat_rate"`
	Charges  []charge `yaml:"charges"`
}

type charge struct {
	ID      string         `yaml:"id"`
	VAT     domain.VATMode `yaml:"vat"`
	Periods []chargePeriod `yaml:"periods"`
}

type chargePeriod struct {
	metadata `yaml:",inline"`
	Value    *float64 `yaml:"value"`
}

// Load validates and compiles the selected YAML profiles once at startup.
func Load(files fs.FS, providerID, gridID, region string) (domain.PricingRules, error) {
	if !identifier.MatchString(providerID) || !identifier.MatchString(gridID) {
		return domain.PricingRules{}, fmt.Errorf("invalid provider or grid profile identifier")
	}
	var provider providerFile
	var grid gridFile
	var national nationalFile
	if err := decode(files, "providers/"+providerID+".yaml", &provider); err != nil {
		return domain.PricingRules{}, err
	}
	if err := decode(files, "grids/"+gridID+".yaml", &grid); err != nil {
		return domain.PricingRules{}, err
	}
	if err := decode(files, "dk/national.yaml", &national); err != nil {
		return domain.PricingRules{}, err
	}
	if provider.Version != 1 || grid.Version != 1 || national.Version != 1 ||
		provider.Unit != "ore_per_kwh" || grid.Unit != "ore_per_kwh" || national.Unit != "ore_per_kwh" ||
		provider.ID != providerID || grid.Operator != gridID || grid.CustomerGroup != "C" || national.Country != "DK" ||
		!provider.MarkupVAT.Valid() || !grid.VAT.Valid() || !contains(provider.Regions, region) || !contains(grid.Regions, region) ||
		grid.Timezone != "Europe/Copenhagen" || national.Timezone != grid.Timezone || !validValue(national.VATRate) || *national.VATRate > 1 {
		return domain.PricingRules{}, fmt.Errorf("invalid or incompatible provider/grid/national profile for %s", region)
	}
	if err := checkSource(provider.Source, provider.VerifiedAt); err != nil {
		return domain.PricingRules{}, err
	}
	location, err := time.LoadLocation(grid.Timezone)
	if err != nil {
		return domain.PricingRules{}, err
	}
	rules := domain.PricingRules{
		Region:   region,
		Provider: providerID, GridOperator: gridID, CustomerGroup: grid.CustomerGroup,
		Location: location, DefaultMarkupVAT: provider.MarkupVAT, VATRate: *national.VATRate, GridVAT: grid.VAT,
	}
	for _, period := range grid.Periods {
		window, err := compileWindow(period.metadata, location)
		if err != nil {
			return rules, err
		}
		compiled := domain.GridPeriod{Window: window}
		// Keep compilation deterministic despite YAML's named season map.
		var names []string
		for name := range period.Seasons {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			s := period.Seasons[name]
			if !monthDay(s.From) || !monthDay(s.Until) || s.From == s.Until {
				return rules, fmt.Errorf("invalid season %s", name)
			}
			rates, err := compileRates(s.Rates)
			if err != nil {
				return rules, fmt.Errorf("season %s: %w", name, err)
			}
			compiled.Seasons = append(compiled.Seasons, domain.Season{From: s.From, Until: s.Until, Rates: rates})
		}
		for date := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC); date.Year() == 2000; date = date.AddDate(0, 0, 1) {
			matches := 0
			for _, s := range compiled.Seasons {
				if s.Contains(date.Format("01-02")) {
					matches++
				}
			}
			if matches != 1 {
				return rules, fmt.Errorf("season gap or overlap on %s", date.Format("01-02"))
			}
		}
		rules.GridPeriods = append(rules.GridPeriods, compiled)
	}
	var gridWindows []domain.Window
	for _, period := range rules.GridPeriods {
		gridWindows = append(gridWindows, period.Window)
	}
	if err := checkWindows(gridWindows); err != nil {
		return rules, fmt.Errorf("grid periods: %w", err)
	}
	ids := map[string]bool{"spot_price": true, "spot_price_markup": true, "grid_tariff": true, "current_price": true, "vat": true,
		"total_excl_vat": true, "valid_from": true, "valid_until": true, "provider": true, "grid_operator": true,
		"customer_group": true, "region": true, "unit": true, "resolution": true, "estimated": true, "components_ore": true, "prices": true}
	for _, charge := range national.Charges {
		if !identifier.MatchString(charge.ID) || ids[charge.ID] || !charge.VAT.Valid() {
			return rules, fmt.Errorf("invalid/duplicate national charge %q", charge.ID)
		}
		ids[charge.ID] = true
		compiled := domain.ChargeProfile{ID: charge.ID, VAT: charge.VAT}
		var windows []domain.Window
		for _, period := range charge.Periods {
			if !validValue(period.Value) {
				return rules, fmt.Errorf("missing or invalid %s value", charge.ID)
			}
			window, err := compileWindow(period.metadata, location)
			if err != nil {
				return rules, err
			}
			windows = append(windows, window)
			compiled.Periods = append(compiled.Periods, domain.ChargePeriod{Window: window, Value: *period.Value})
		}
		if err := checkWindows(windows); err != nil {
			return rules, fmt.Errorf("%s: %w", charge.ID, err)
		}
		rules.NationalCharges = append(rules.NationalCharges, compiled)
	}
	for _, required := range []string{"transmission_tariff", "system_tariff", "electricity_tax"} {
		if !ids[required] {
			return rules, fmt.Errorf("missing national charge %s", required)
		}
	}
	return rules, nil
}

func decode(files fs.FS, name string, destination any) error {
	file, err := files.Open(name)
	if err != nil {
		return fmt.Errorf("load profile %s: %w", name, err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.KnownFields(true)
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode profile %s: %w", name, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected trailing profile data in %s", name)
	}
	return nil
}

func compileWindow(meta metadata, location *time.Location) (domain.Window, error) {
	if err := checkSource(meta.Source, meta.VerifiedAt); err != nil {
		return domain.Window{}, err
	}
	from, err := time.ParseInLocation(time.DateOnly, meta.From, location)
	if err != nil {
		return domain.Window{}, fmt.Errorf("invalid valid_from %q", meta.From)
	}
	until, err := time.ParseInLocation(time.DateOnly, meta.Until, location)
	if err != nil || !until.After(from) {
		return domain.Window{}, fmt.Errorf("invalid valid_until %q", meta.Until)
	}
	return domain.Window{From: from, Until: until}, nil
}

func compileRates(rates []rate) ([24]float64, error) {
	var result [24]float64
	var assigned [24]bool
	for _, rate := range rates {
		from, err := hour(rate.From, false)
		if err != nil {
			return result, err
		}
		until, err := hour(rate.Until, true)
		if err != nil || until <= from || !validValue(rate.Value) {
			return result, fmt.Errorf("invalid interval or value %s-%s", rate.From, rate.Until)
		}
		for h := from; h < until; h++ {
			if assigned[h] {
				return result, fmt.Errorf("overlapping hour %d", h)
			}
			result[h], assigned[h] = *rate.Value, true
		}
	}
	for h, set := range assigned {
		if !set {
			return result, fmt.Errorf("missing hour %d", h)
		}
	}
	return result, nil
}

func checkWindows(windows []domain.Window) error {
	if len(windows) == 0 {
		return fmt.Errorf("no price periods")
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].From.Before(windows[j].From) })
	for i := 1; i < len(windows); i++ {
		if windows[i].From.Before(windows[i-1].Until) {
			return fmt.Errorf("overlapping price periods")
		}
	}
	return nil
}

func hour(value string, allow24 bool) (int, error) {
	if len(value) != 5 || !strings.HasSuffix(value, ":00") {
		return 0, fmt.Errorf("invalid hourly boundary %q", value)
	}
	h, err := strconv.Atoi(value[:2])
	if err != nil || h < 0 || h > 24 || (h == 24 && !allow24) {
		return 0, fmt.Errorf("invalid hour %q", value)
	}
	return h, nil
}

func monthDay(value string) bool {
	if len(value) != 5 {
		return false
	}
	_, err := time.Parse("2006-01-02", "2000-"+value)
	return err == nil
}

func checkSource(source, verified string) error {
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("missing or invalid profile source")
	}
	if _, err := time.Parse(time.DateOnly, verified); err != nil {
		return fmt.Errorf("invalid verified_at date")
	}
	return nil
}

func validValue(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
}
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
