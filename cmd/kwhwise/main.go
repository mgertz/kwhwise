package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mgertz/kwhwise/internal/adapters/energinet"
	"github.com/mgertz/kwhwise/internal/adapters/mqttpublisher"
	"github.com/mgertz/kwhwise/internal/adapters/profiledata"
	"github.com/mgertz/kwhwise/internal/application"
	"github.com/mgertz/kwhwise/internal/buildinfo"
	"github.com/mgertz/kwhwise/internal/config"
	"github.com/mgertz/kwhwise/internal/domain"
	"github.com/mgertz/kwhwise/internal/scheduler"
	"github.com/mgertz/kwhwise/profiles"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.Version)
		return
	}
	configuration, err := config.Load()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	rules, err := profiledata.Load(profiles.Files, configuration.ElectricityProvider, configuration.GridOperator, configuration.Region)
	if err != nil {
		log.Fatalf("profile error: %v", err)
	}
	calculator, err := domain.NewCalculator(rules, configuration.SpotPriceMarkupOre, domain.VATMode(configuration.SpotPriceMarkupVAT))
	if err != nil {
		log.Fatalf("pricing error: %v", err)
	}
	// Set the process default before starting workers, including log timestamps.
	time.Local = configuration.Timezone

	log.Printf("kwhwise %s starting for %s in %s", buildinfo.Version, configuration.Region, configuration.Timezone)

	// Docker sends SIGTERM on stop. Cancellation also reaches future HTTP/MQTT work.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpClient := &http.Client{Timeout: 10 * time.Second}
	defer httpClient.CloseIdleConnections()
	source := energinet.NewClient(httpClient, energinet.Endpoint, configuration.TariffTimezone)
	spotPrices := application.NewSpotPrices(source, configuration.Region, configuration.TariffTimezone)
	prices := application.NewPrices(spotPrices, calculator)
	publisher := mqttpublisher.New(configuration.MQTT, rules, log.Default())
	publisher.Start()
	defer publisher.Close()
	var sink application.PricePublisher = publisher

	// Follow the Danish price boundaries even if TZ is used to display logs
	// in a different zone; otherwise half-hour-offset zones could lag prices.
	scheduler.RunWithRetry(ctx, rules.Location, func(ctx context.Context, triggeredAt time.Time) error {
		updateCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		snapshot, err := prices.At(updateCtx, triggeredAt)
		if err == nil {
			// A startup HTTP request may finish in the next hour. Select again
			// from the cache if the fetched hour expired while waiting.
			now := time.Now()
			if now.Before(snapshot.Current.Start) || !now.Before(snapshot.Current.End) {
				snapshot, err = prices.At(updateCtx, now)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// Use a fresh context so a timed-out API call can still mark MQTT
			// unavailable rather than leaving an expired price online.
			statusCtx, statusCancel := context.WithTimeout(ctx, 3*time.Second)
			_ = sink.Unavailable(statusCtx)
			statusCancel()
			log.Printf("price unavailable: %v", err)
			return err
		}
		if err := sink.Publish(updateCtx, snapshot); err != nil {
			log.Printf("MQTT publish delayed/failed: %v", err)
			return err
		}
		log.Printf("published %s/%s total price: %.6f DKK/kWh including VAT (valid %s to %s)",
			rules.Provider, rules.GridOperator, snapshot.Current.TotalInclVAT/100,
			snapshot.Current.Start.In(configuration.Timezone).Format(time.RFC3339),
			snapshot.Current.End.In(configuration.Timezone).Format(time.RFC3339))
		log.Printf("next update at %s", scheduler.NextHour(time.Now(), rules.Location).In(configuration.Timezone).Format(time.RFC3339))
		return nil
	})
	log.Println("kwhwise stopped")
}
