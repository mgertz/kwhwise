package energinet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHourlyPricesRequestsDanishDayAndAveragesQuarters(t *testing.T) {
	location := danishLocation(t)
	day := time.Date(2026, 10, 8, 22, 30, 0, 0, time.UTC) // October 9 in Denmark.
	data := fixture(t, day)
	for i, price := range []float64{728.107323, 713.383733, 693.876846, 663.233871} {
		data.Records[i].PriceDKK = &price
	}
	// The API order must not determine which quarters belong to an hour.
	for left, right := 0, len(data.Records)-1; left < right; left, right = left+1, right-1 {
		data.Records[left], data.Records[right] = data.Records[right], data.Records[left]
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || query.Get("start") != "2026-10-09" || query.Get("end") != "2026-10-10" ||
			query.Get("sort") != "TimeUTC" || query.Get("limit") != "100" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		var filter map[string][]string
		if err := json.Unmarshal([]byte(query.Get("filter")), &filter); err != nil ||
			len(filter["PriceArea"]) != 1 || filter["PriceArea"][0] != "DK2" {
			t.Errorf("unexpected filter: %s", query.Get("filter"))
		}
		writeJSON(t, w, data)
	}))
	defer server.Close()
	client := NewClient(server.Client(), server.URL, location)
	prices, err := client.HourlyPrices(context.Background(), "DK2", day)
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 24 {
		t.Fatalf("got %d hourly prices, want 24", len(prices))
	}
	// Four real public API prices, converted without adding VAT.
	if got := prices[0].PriceOreExclVAT; math.Abs(got-69.965044325) > 1e-9 {
		t.Errorf("first hourly price = %.10f, want 69.965044325 ore excluding VAT", got)
	}
	if want := time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC); !prices[0].Start.Equal(want) ||
		!prices[0].End.Equal(want.Add(time.Hour)) || prices[0].Start.Location() != time.UTC {
		t.Errorf("unexpected first price interval: %+v", prices[0])
	}
}

func TestHourlyPricesHandlesDaylightSavingAndNegativePrices(t *testing.T) {
	for _, test := range []struct {
		name  string
		month time.Month
		day   int
		hours int
	}{
		{"summer time", time.March, 29, 23},
		{"winter time", time.October, 25, 25},
	} {
		t.Run(test.name, func(t *testing.T) {
			location := danishLocation(t)
			day := time.Date(2026, test.month, test.day, 12, 0, 0, 0, location)
			data := fixture(t, day)
			negativePrice := -250.0
			for i := 0; i < 4; i++ {
				data.Records[i].PriceDKK = &negativePrice
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, data)
			}))
			defer server.Close()
			prices, err := NewClient(server.Client(), server.URL, location).HourlyPrices(context.Background(), "DK2", day)
			if err != nil {
				t.Fatal(err)
			}
			if len(prices) != test.hours || prices[0].PriceOreExclVAT != -25 {
				t.Fatalf("got %d hours, first price %v; want %d hours, first price -25", len(prices), prices[0].PriceOreExclVAT, test.hours)
			}
			for i := 1; i < len(prices); i++ {
				if !prices[i].Start.Equal(prices[i-1].End) {
					t.Errorf("non-contiguous intervals: %+v and %+v", prices[i-1], prices[i])
				}
			}
			if test.hours == 25 {
				first, second := prices[2].Start.In(location), prices[3].Start.In(location)
				_, firstOffset := first.Zone()
				_, secondOffset := second.Zone()
				if first.Hour() != 2 || second.Hour() != 2 || firstOffset == secondOffset {
					t.Errorf("repeated local hour not preserved: %s and %s", first, second)
				}
			}
		})
	}
}

func TestHourlyPricesRejectsInvalidData(t *testing.T) {
	day := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		change func(*response)
		want   string
	}{
		{"missing quarter", func(data *response) { data.Records = data.Records[1:] }, "incomplete"},
		{"empty day", func(data *response) { data.Records = nil }, "incomplete"},
		{"duplicate quarter", func(data *response) { data.Records[1] = data.Records[0] }, "duplicate"},
		{"missing price", func(data *response) { data.Records[0].PriceDKK = nil }, "missing or invalid price"},
		{"wrong area", func(data *response) { data.Records[0].PriceArea = "DK1" }, "unexpected price area"},
		{"wrong dataset", func(data *response) { data.Dataset = "Elspotprices" }, "unexpected Energinet dataset"},
		{"invalid timestamp", func(data *response) { data.Records[0].TimeUTC = "invalid" }, "invalid UTC timestamp"},
		{"misaligned timestamp", func(data *response) { data.Records[0].TimeUTC = "2026-10-08T22:01:00" }, "unexpected quarter"},
		{"out of range timestamp", func(data *response) { data.Records[0].TimeUTC = "2026-10-08T21:45:00" }, "unexpected quarter"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := fixture(t, day)
			test.change(&data)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, data)
			}))
			defer server.Close()
			_, err := NewClient(server.Client(), server.URL, danishLocation(t)).HourlyPrices(context.Background(), "DK2", day)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHourlyPricesHandlesHTTPAndDecodingErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"server error", http.StatusServiceUnavailable, "unavailable", "HTTP 503"},
		{"malformed JSON", http.StatusOK, "{", "decode Energinet prices"},
		{"trailing JSON", http.StatusOK, `{"dataset":"DayAheadPrices","records":[]} {}`, "trailing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			_, err := NewClient(server.Client(), server.URL, danishLocation(t)).HourlyPrices(context.Background(), "DK2", time.Now())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHourlyPricesRespectsRateLimitCooldown(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := NewClient(server.Client(), server.URL, danishLocation(t))
	for i := 0; i < 2; i++ {
		if _, err := client.HourlyPrices(context.Background(), "DK2", time.Now()); err == nil {
			t.Fatal("expected rate limit error")
		}
	}
	if calls != 1 {
		t.Fatalf("made %d HTTP calls during cooldown, want 1", calls)
	}
}

func TestHourlyPricesPropagatesCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("cancelled request reached server")
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewClient(server.Client(), server.URL, danishLocation(t)).HourlyPrices(ctx, "DK2", time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestHourlyPricesCanBeCancelledDuringRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewClient(server.Client(), server.URL, danishLocation(t))
	done := make(chan error, 1)
	go func() {
		_, err := client.HourlyPrices(ctx, "DK2", time.Now())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not stop after cancellation")
	}
}

func TestRetryDeadline(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		header string
		want   time.Time
	}{
		{"120", now.Add(2 * time.Minute)},
		{now.Add(time.Hour).Format(http.TimeFormat), now.Add(time.Hour)},
		{"", now.Add(5 * time.Minute)},
		{"-1", now.Add(5 * time.Minute)},
		{"999999999999999999999999999", now.Add(5 * time.Minute)},
	} {
		if got := retryDeadline(test.header, now); !got.Equal(test.want) {
			t.Errorf("retryDeadline(%q) = %s, want %s", test.header, got, test.want)
		}
	}
}

func fixture(t *testing.T, day time.Time) response {
	t.Helper()
	location := danishLocation(t)
	day = day.In(location)
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 1)
	data := response{Dataset: "DayAheadPrices"}
	for timestamp := start.UTC(); timestamp.Before(end); timestamp = timestamp.Add(15 * time.Minute) {
		price := 1000.0
		data.Records = append(data.Records, record{
			TimeUTC: timestamp.Format("2006-01-02T15:04:05"), PriceArea: "DK2", PriceDKK: &price,
		})
	}
	return data
}

func danishLocation(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func writeJSON(t *testing.T, w http.ResponseWriter, data response) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		t.Error(err)
	}
}
