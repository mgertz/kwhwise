package profiledata

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mgertz/kwhwise/profiles"
)

func TestLoadRejectsIncompatibleRegionAndUnknownProfiles(t *testing.T) {
	for _, test := range []struct{ provider, grid, region string }{
		{"nrgi", "cerius", "DK1"}, {"missing", "cerius", "DK2"}, {"nrgi", "missing", "DK2"}, {"../nrgi", "cerius", "DK2"},
	} {
		if _, err := Load(profiles.Files, test.provider, test.grid, test.region); err == nil {
			t.Errorf("accepted invalid selection: %+v", test)
		}
	}
}

func TestLoadRejectsBrokenProfiles(t *testing.T) {
	for _, test := range []struct{ name, file, old, replacement string }{
		{"unknown field", "grids/cerius.yaml", "customer_group:", "unknown_group:"},
		{"wrong unit", "grids/cerius.yaml", "ore_per_kwh", "dkk_per_kwh"},
		{"wrong customer group", "grids/cerius.yaml", "customer_group: C", "customer_group: B"},
		{"missing hour", "grids/cerius.yaml", `to: "06:00"`, `to: "05:00"`},
		{"overlapping hour", "grids/cerius.yaml", `to: "06:00"`, `to: "07:00"`},
		{"fractional hour", "grids/cerius.yaml", `to: "06:00"`, `to: "06:30"`},
		{"season gap", "grids/cerius.yaml", `until: "10-01"`, `until: "09-01"`},
		{"season overlap", "grids/cerius.yaml", `until: "10-01"`, `until: "11-01"`},
		{"bad date", "grids/cerius.yaml", `valid_until: "2027-04-01"`, `valid_until: "2026-01-01"`},
		{"nonfinite price", "grids/cerius.yaml", "value: 11.5336", "value: .nan"},
		{"missing price", "grids/cerius.yaml", "value: 11.5336", "value: null"},
		{"bad VAT", "dk/national.yaml", "vat: excluded", "vat: invalid"},
		{"missing national charge", "dk/national.yaml", "id: electricity_tax", "id: other_tax"},
		{"duplicate component", "dk/national.yaml", "id: electricity_tax", "id: system_tariff"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := fstest.MapFS{}
			for _, file := range []string{"providers/nrgi.yaml", "grids/cerius.yaml", "dk/national.yaml"} {
				data, err := fs.ReadFile(profiles.Files, file)
				if err != nil {
					t.Fatal(err)
				}
				if file == test.file {
					data = []byte(strings.Replace(string(data), test.old, test.replacement, 1))
				}
				files[file] = &fstest.MapFile{Data: data}
			}
			if _, err := Load(files, "nrgi", "cerius", "DK2"); err == nil {
				t.Fatal("accepted invalid profile")
			}
		})
	}
}
