package config

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Include timezone data even in minimal container images.
)

const defaultTimezone = "Europe/Copenhagen"

type Config struct {
	Region              string
	ElectricityProvider string
	GridOperator        string
	SpotPriceMarkupOre  float64
	SpotPriceMarkupVAT  string
	Timezone            *time.Location
	TariffTimezone      *time.Location
	MQTT                MQTT
}

type MQTT struct {
	Host            string
	Port            int
	Username        string
	Password        string
	ClientID        string
	BaseTopic       string
	DiscoveryPrefix string
	TLS             bool
}

var profileID = regexp.MustCompile(`^[a-z0-9_-]+$`)
var clientID = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var topic = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)

func Load() (Config, error) {
	region := strings.ToUpper(getEnv("REGION", ""))
	if region != "DK1" && region != "DK2" {
		return Config{}, fmt.Errorf("REGION must be DK1 or DK2, got %q", region)
	}
	provider := strings.ToLower(getEnv("ELECTRICITY_PROVIDER", ""))
	if !profileID.MatchString(provider) {
		return Config{}, fmt.Errorf("ELECTRICITY_PROVIDER must be a profile identifier, such as nrgi")
	}

	gridOperator := strings.ToLower(getEnv("GRID_OPERATOR", ""))
	if !profileID.MatchString(gridOperator) {
		return Config{}, fmt.Errorf("GRID_OPERATOR must be a profile identifier, such as cerius")
	}

	markup, err := strconv.ParseFloat(getEnv("SPOT_PRICE_MARKUP_ORE", ""), 64)
	if err != nil || math.IsNaN(markup) || math.IsInf(markup, 0) || markup < 0 {
		return Config{}, fmt.Errorf("SPOT_PRICE_MARKUP_ORE must be a finite, non-negative number")
	}

	// An omitted mode uses the selected provider profile's documented default.
	markupVAT := strings.ToLower(getEnv("SPOT_PRICE_MARKUP_VAT", ""))
	switch markupVAT {
	case "", "included", "excluded", "none":
	default:
		return Config{}, fmt.Errorf("SPOT_PRICE_MARKUP_VAT must be included, excluded or none, got %q", markupVAT)
	}
	mqtt, err := loadMQTT()
	if err != nil {
		return Config{}, err
	}

	timezoneName := getEnv("TZ", defaultTimezone)
	timezone, err := time.LoadLocation(timezoneName)
	if err != nil {
		return Config{}, fmt.Errorf("invalid TZ %q: %w", timezoneName, err)
	}

	// Danish tariffs always use Danish local time, independently of TZ.
	tariffTimezone, err := time.LoadLocation(defaultTimezone)
	if err != nil {
		return Config{}, fmt.Errorf("load Danish tariff timezone: %w", err)
	}

	return Config{
		Region:              region,
		ElectricityProvider: provider,
		GridOperator:        gridOperator,
		SpotPriceMarkupOre:  markup,
		SpotPriceMarkupVAT:  markupVAT,
		Timezone:            timezone,
		TariffTimezone:      tariffTimezone,
		MQTT:                mqtt,
	}, nil
}

func loadMQTT() (MQTT, error) {
	result := MQTT{
		Host: getEnv("MQTT_HOST", ""), Username: os.Getenv("MQTT_USERNAME"), Password: os.Getenv("MQTT_PASSWORD"),
		ClientID: getEnv("MQTT_CLIENT_ID", "kwhwise"),
	}
	if result.Host == "" || strings.ContainsAny(result.Host, "/@ \t\r\n\x00") {
		return result, fmt.Errorf("MQTT_HOST must be a hostname or IP address")
	}
	if result.Password != "" && result.Username == "" {
		return result, fmt.Errorf("MQTT_USERNAME is required when MQTT_PASSWORD is set")
	}
	if !clientID.MatchString(result.ClientID) {
		return result, fmt.Errorf("MQTT_CLIENT_ID must use lowercase letters, digits and underscores, starting with a letter")
	}
	result.BaseTopic = getEnv("MQTT_BASE_TOPIC", result.ClientID)
	result.DiscoveryPrefix = getEnv("MQTT_DISCOVERY_PREFIX", "homeassistant")
	if !topic.MatchString(result.BaseTopic) || !topic.MatchString(result.DiscoveryPrefix) || result.BaseTopic == result.DiscoveryPrefix {
		return result, fmt.Errorf("MQTT_BASE_TOPIC and MQTT_DISCOVERY_PREFIX must be different valid topic paths without wildcards")
	}
	port, err := strconv.Atoi(getEnv("MQTT_PORT", "1883"))
	if err != nil || port < 1 || port > 65535 {
		return result, fmt.Errorf("MQTT_PORT must be between 1 and 65535")
	}
	result.Port = port
	result.TLS, err = strconv.ParseBool(getEnv("MQTT_TLS", "false"))
	if err != nil {
		return result, fmt.Errorf("MQTT_TLS must be true or false")
	}
	return result, nil
}

func getEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
