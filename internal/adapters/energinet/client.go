package energinet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mgertz/kwhwise/internal/domain"
)

const Endpoint = "https://api.energidataservice.dk/dataset/DayAheadPrices"

// Client is the HTTP adapter for Energinet's day-ahead spot prices.
// Like the application service, it is used sequentially by the scheduler.
type Client struct {
	httpClient *http.Client
	endpoint   string
	location   *time.Location
	retryAt    time.Time
}

type RateLimitError struct{ Until time.Time }

func (e *RateLimitError) Error() string {
	return "Energinet HTTP 429: retry after " + e.Until.Format(time.RFC3339)
}
func (e *RateLimitError) RetryAt() time.Time { return e.Until }

func NewClient(httpClient *http.Client, endpoint string, location *time.Location) *Client {
	return &Client{httpClient: httpClient, endpoint: endpoint, location: location}
}

type response struct {
	Dataset string   `json:"dataset"`
	Records []record `json:"records"`
}

type record struct {
	TimeUTC   string   `json:"TimeUTC"`
	PriceArea string   `json:"PriceArea"`
	PriceDKK  *float64 `json:"DayAheadPriceDKK"`
}

// HourlyPrices fetches a complete Danish day and averages four quarters per
// hour. The API accepts Danish local dates; response timestamps are UTC.
func (c *Client) HourlyPrices(ctx context.Context, region string, day time.Time) ([]domain.SpotPrice, error) {
	if region != "DK1" && region != "DK2" {
		return nil, fmt.Errorf("invalid price area %q", region)
	}
	if time.Now().Before(c.retryAt) {
		return nil, &RateLimitError{Until: c.retryAt}
	}
	localDay := day.In(c.location)
	start := time.Date(localDay.Year(), localDay.Month(), localDay.Day(), 0, 0, 0, 0, c.location)
	end := start.AddDate(0, 0, 1)

	u, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Energinet endpoint: %w", err)
	}
	filter, err := json.Marshal(map[string][]string{"PriceArea": {region}})
	if err != nil {
		return nil, fmt.Errorf("encode price area filter: %w", err)
	}
	query := u.Query()
	query.Set("start", start.Format(time.DateOnly))
	query.Set("end", end.Format(time.DateOnly))
	query.Set("filter", string(filter))
	query.Set("sort", "TimeUTC")
	query.Set("limit", "100") // A 25-hour winter-time day contains 100 quarters.
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Energinet request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kwhwise/0.1 (github.com/mgertz/kwhwise)")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch Energinet prices: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests {
			c.retryAt = retryDeadline(resp.Header.Get("Retry-After"), time.Now())
			return nil, &RateLimitError{Until: c.retryAt}
		}
		return nil, fmt.Errorf("Energinet HTTP %d", resp.StatusCode)
	}

	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	var result response
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode Energinet prices: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing Energinet response data")
	}
	if result.Dataset != "DayAheadPrices" {
		return nil, fmt.Errorf("unexpected Energinet dataset %q", result.Dataset)
	}
	return aggregate(result.Records, region, start.UTC(), end.UTC())
}

func aggregate(records []record, region string, start, end time.Time) ([]domain.SpotPrice, error) {
	expectedQuarters := int(end.Sub(start) / (15 * time.Minute))
	if len(records) != expectedQuarters {
		return nil, fmt.Errorf("incomplete Energinet day: got %d quarters, expected %d", len(records), expectedQuarters)
	}
	quarters := make(map[time.Time]float64, len(records))
	for _, row := range records {
		if row.PriceArea != region {
			return nil, fmt.Errorf("unexpected price area %q, expected %s", row.PriceArea, region)
		}
		if row.PriceDKK == nil || math.IsNaN(*row.PriceDKK) || math.IsInf(*row.PriceDKK, 0) {
			return nil, fmt.Errorf("missing or invalid price at %s", row.TimeUTC)
		}
		// The API omits Z; time.Parse interprets this unzoned layout as UTC.
		timestamp, err := time.Parse("2006-01-02T15:04:05", row.TimeUTC)
		if err != nil {
			return nil, fmt.Errorf("invalid UTC timestamp %q: %w", row.TimeUTC, err)
		}
		if timestamp.Before(start) || !timestamp.Before(end) || timestamp.Minute()%15 != 0 || timestamp.Second() != 0 {
			return nil, fmt.Errorf("unexpected quarter timestamp %s", row.TimeUTC)
		}
		if _, exists := quarters[timestamp]; exists {
			return nil, fmt.Errorf("duplicate quarter at %s", row.TimeUTC)
		}
		quarters[timestamp] = *row.PriceDKK
	}

	prices := make([]domain.SpotPrice, 0, expectedQuarters/4)
	for hour := start; hour.Before(end); hour = hour.Add(time.Hour) {
		var priceOre float64
		for quarter := 0; quarter < 4; quarter++ {
			timestamp := hour.Add(time.Duration(quarter) * 15 * time.Minute)
			price, exists := quarters[timestamp]
			if !exists {
				return nil, fmt.Errorf("missing quarter at %s", timestamp.Format(time.RFC3339))
			}
			priceOre += price / 40 // DKK/MWh / 10 / 4 = hourly average in ore/kWh.
		}
		if math.IsNaN(priceOre) || math.IsInf(priceOre, 0) {
			return nil, fmt.Errorf("invalid hourly price at %s", hour.Format(time.RFC3339))
		}
		prices = append(prices, domain.SpotPrice{
			Start: hour, End: hour.Add(time.Hour), PriceOreExclVAT: priceOre,
		})
	}
	return prices, nil
}

func retryDeadline(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/int64(time.Second)) {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		return deadline
	}
	// A missing or malformed header must not cause immediate repeated requests.
	return now.Add(5 * time.Minute)
}
