package config

import (
	"strings"
	"testing"
)

func TestLoadUsesDanishDefaults(t *testing.T) {
	setValidEnvironment(t)

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if configuration.Region != "DK2" {
		t.Errorf("Region = %q, want DK2", configuration.Region)
	}
	if configuration.Timezone.String() != "Europe/Copenhagen" {
		t.Errorf("Timezone = %q, want Europe/Copenhagen", configuration.Timezone)
	}
	if configuration.SpotPriceMarkupOre != 9 || configuration.SpotPriceMarkupVAT != "" {
		t.Errorf("unexpected default markup: %v (%s)", configuration.SpotPriceMarkupOre, configuration.SpotPriceMarkupVAT)
	}
	if configuration.MQTT.Port != 1883 || configuration.MQTT.BaseTopic != "kwhwise" || configuration.MQTT.TLS {
		t.Errorf("unexpected MQTT defaults: %+v", configuration.MQTT)
	}
}

func TestLoadNormalizesConfiguration(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("REGION", " dk1 ")
	t.Setenv("GRID_OPERATOR", " KONSTANT ")
	t.Setenv("SPOT_PRICE_MARKUP_ORE", " 9.25 ")
	t.Setenv("SPOT_PRICE_MARKUP_VAT", " INCLUDED ")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if configuration.Region != "DK1" || configuration.GridOperator != "konstant" ||
		configuration.SpotPriceMarkupOre != 9.25 || configuration.SpotPriceMarkupVAT != "included" {
		t.Errorf("unexpected normalized configuration: %+v", configuration)
	}
}

func TestLoadKeepsDanishTariffTimezoneWhenTZIsOverridden(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("TZ", "Asia/Kolkata")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if configuration.Timezone.String() != "Asia/Kolkata" {
		t.Errorf("Timezone = %s, want Asia/Kolkata", configuration.Timezone)
	}
	if configuration.TariffTimezone.String() != "Europe/Copenhagen" {
		t.Errorf("TariffTimezone = %s, want Europe/Copenhagen", configuration.TariffTimezone)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"missing region", "REGION", ""},
		{"invalid region", "REGION", "DK3"},
		{"missing provider", "ELECTRICITY_PROVIDER", ""},
		{"unsafe provider", "ELECTRICITY_PROVIDER", "../nrgi"},
		{"missing grid operator", "GRID_OPERATOR", ""},
		{"blank grid operator", "GRID_OPERATOR", "   "},
		{"missing markup", "SPOT_PRICE_MARKUP_ORE", ""},
		{"negative markup", "SPOT_PRICE_MARKUP_ORE", "-1"},
		{"not a number", "SPOT_PRICE_MARKUP_ORE", "NaN"},
		{"positive infinity", "SPOT_PRICE_MARKUP_ORE", "+Inf"},
		{"negative infinity", "SPOT_PRICE_MARKUP_ORE", "-Inf"},
		{"overflow", "SPOT_PRICE_MARKUP_ORE", "1e999"},
		{"invalid markup", "SPOT_PRICE_MARKUP_ORE", "nine"},
		{"invalid VAT", "SPOT_PRICE_MARKUP_VAT", "unknown"},
		{"invalid timezone", "TZ", "Not/AZone"},
		{"missing broker", "MQTT_HOST", ""},
		{"broker URL instead of host", "MQTT_HOST", "tcp://localhost"},
		{"bad port", "MQTT_PORT", "65536"},
		{"zero port", "MQTT_PORT", "0"},
		{"bad TLS", "MQTT_TLS", "perhaps"},
		{"bad instance ID", "MQTT_CLIENT_ID", "a/b"},
		{"wildcard base topic", "MQTT_BASE_TOPIC", "prices/#"},
		{"wildcard discovery prefix", "MQTT_DISCOVERY_PREFIX", "homeassistant/+"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.key, test.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Load() error = %v, want error mentioning %s", err, test.key)
			}
		})
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("REGION", "DK2")
	t.Setenv("ELECTRICITY_PROVIDER", "nrgi")
	t.Setenv("GRID_OPERATOR", "cerius")
	t.Setenv("SPOT_PRICE_MARKUP_ORE", "9")
	t.Setenv("SPOT_PRICE_MARKUP_VAT", "")
	t.Setenv("TZ", "")
	t.Setenv("MQTT_HOST", "localhost")
	t.Setenv("MQTT_PORT", "")
	t.Setenv("MQTT_USERNAME", "")
	t.Setenv("MQTT_PASSWORD", "")
	t.Setenv("MQTT_CLIENT_ID", "")
	t.Setenv("MQTT_BASE_TOPIC", "")
	t.Setenv("MQTT_DISCOVERY_PREFIX", "")
	t.Setenv("MQTT_TLS", "")
}
