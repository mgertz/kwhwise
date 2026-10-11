package mqttpublisher

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/mgertz/kwhwise/internal/application"
	"github.com/mgertz/kwhwise/internal/buildinfo"
	"github.com/mgertz/kwhwise/internal/config"
	"github.com/mgertz/kwhwise/internal/domain"
)

type connection interface {
	Connect() mqtt.Token
	Disconnect(uint)
	IsConnectionOpen() bool
	Publish(string, byte, bool, interface{}) mqtt.Token
}

type Publisher struct {
	client connection
	cfg    config.MQTT
	rules  domain.PricingRules
	logger *log.Logger
	mu     sync.Mutex
	latest []byte
	from   time.Time
	until  time.Time
	expiry *time.Timer
	closed bool
}

func New(cfg config.MQTT, rules domain.PricingRules, logger *log.Logger) *Publisher {
	p := &Publisher{cfg: cfg, rules: rules, logger: logger}
	scheme := "tcp"
	if cfg.TLS {
		scheme = "ssl"
	}
	opts := mqtt.NewClientOptions().AddBroker(scheme+"://"+net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))).
		SetClientID(cfg.ClientID).SetUsername(cfg.Username).SetPassword(cfg.Password).
		SetKeepAlive(30*time.Second).SetPingTimeout(5*time.Second).SetWriteTimeout(5*time.Second).
		SetConnectTimeout(5*time.Second).SetAutoReconnect(true).SetMaxReconnectInterval(30*time.Second).
		SetConnectRetry(true).SetConnectRetryInterval(5*time.Second).SetOrderMatters(false).
		SetWill(cfg.BaseTopic+"/availability", "offline", 1, true)
	if cfg.TLS {
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Re-send discovery/current state when Home Assistant starts as well.
		if err := wait(ctx, client.Subscribe(cfg.DiscoveryPrefix+"/status", 1, func(_ mqtt.Client, message mqtt.Message) {
			if string(message.Payload()) == "online" {
				p.resync()
			}
		})); err != nil {
			logger.Printf("MQTT birth subscription failed: %v", err)
		}
		p.resync()
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) { logger.Printf("MQTT connection lost: %v", err) })
	p.client = mqtt.NewClient(opts)
	return p
}

// Start keeps connecting in the background, allowing prices to be computed
// while the broker is temporarily unavailable. Reconnect replays valid data.
func (p *Publisher) Start() { p.client.Connect() }

func (p *Publisher) Publish(ctx context.Context, snapshot application.Snapshot) error {
	now := time.Now()
	if now.Before(snapshot.Current.Start) || !now.Before(snapshot.Current.End) {
		return fmt.Errorf("refusing to publish a price outside its validity interval")
	}
	data, err := encode(snapshot, p.rules)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("MQTT publisher is closed")
	}
	previousUntil := p.until
	p.latest, p.from, p.until = data, snapshot.Current.Start, snapshot.Current.End
	if p.expiry == nil || !previousUntil.Equal(p.until) {
		p.armExpiry()
	}
	if err := p.sendLatest(ctx); err != nil {
		return err
	}
	// Encoding/locking/acknowledgements may cross a price boundary. Tell the
	// scheduler to retry instead of sleeping a full hour after an expired send.
	if !p.validNow() {
		return fmt.Errorf("price expired while publishing")
	}
	return nil
}

func (p *Publisher) Unavailable(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.latest = nil
	p.stopExpiry()
	if !p.client.IsConnectionOpen() {
		return nil
	}
	return p.send(ctx, p.cfg.BaseTopic+"/availability", "offline", true)
}

func (p *Publisher) Close() {
	p.mu.Lock()
	p.closed = true
	p.latest = nil
	p.stopExpiry()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if p.client.IsConnectionOpen() {
		_ = p.send(ctx, p.cfg.BaseTopic+"/availability", "offline", true)
	}
	cancel()
	p.mu.Unlock()
	p.client.Disconnect(250)
}

func (p *Publisher) resync() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	for _, sensor := range sensors {
		payload, err := json.Marshal(discovery(p.cfg, sensor))
		if err == nil {
			err = p.send(ctx, p.cfg.DiscoveryPrefix+"/sensor/"+p.cfg.ClientID+"/"+sensor.key+"/config", payload, true)
		}
		if err != nil {
			p.logger.Printf("MQTT discovery failed: %v", err)
			return
		}
	}
	if err := p.sendLatest(ctx); err != nil {
		p.logger.Printf("MQTT state sync failed: %v", err)
	}
}

func (p *Publisher) sendLatest(ctx context.Context) error {
	if !p.client.IsConnectionOpen() {
		return fmt.Errorf("MQTT broker disconnected; valid price saved for reconnect")
	}
	if p.latest == nil || !p.validNow() {
		return p.send(ctx, p.cfg.BaseTopic+"/availability", "offline", true)
	}
	// HA creates entities asynchronously after discovery. Retention lets a
	// later subscriber receive the price, without waiting for the next hour.
	// Absolute timestamp availability guards against restoring expired state.
	if err := p.send(ctx, p.cfg.BaseTopic+"/state", p.latest, true); err != nil {
		return err
	}
	if !p.validNow() {
		return p.expired(ctx)
	}
	if err := p.send(ctx, p.cfg.BaseTopic+"/availability", "online", true); err != nil {
		return err
	}
	if !p.validNow() {
		return p.expired(ctx)
	}
	return nil
}

// These helpers are called with mu held. A stopped timer's callback can still
// be waiting for the lock, so its captured deadline must match the current one.
func (p *Publisher) armExpiry() {
	p.stopExpiry()
	deadline := p.until
	p.expiry = time.AfterFunc(time.Until(deadline), func() { p.expireAt(deadline) })
}

func (p *Publisher) stopExpiry() {
	if p.expiry != nil {
		p.expiry.Stop()
		p.expiry = nil
	}
}

func (p *Publisher) expireAt(deadline time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.latest == nil || !deadline.Equal(p.until) {
		return
	}
	if time.Now().Before(p.until) {
		// Account for a backwards wall-clock adjustment since arming the timer.
		p.armExpiry()
		return
	}
	p.stopExpiry()
	if !p.client.IsConnectionOpen() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.send(ctx, p.cfg.BaseTopic+"/availability", "offline", true); err != nil {
		p.logger.Printf("MQTT price expiry notification failed: %v", err)
	}
}

func (p *Publisher) validNow() bool {
	now := time.Now()
	return !now.Before(p.from) && now.Before(p.until)
}

func (p *Publisher) expired(ctx context.Context) error {
	if err := p.send(ctx, p.cfg.BaseTopic+"/availability", "offline", true); err != nil {
		return err
	}
	return fmt.Errorf("price expired while publishing")
}

func (p *Publisher) send(ctx context.Context, topic string, value interface{}, retain bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return wait(ctx, p.client.Publish(topic, 1, retain, value))
}

func wait(ctx context.Context, token mqtt.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token.Done():
		return token.Error()
	}
}

type sensor struct{ key, name string }

var sensors = []sensor{
	{"current_price", "Current price"}, {"spot_price", "Spot price"}, {"spot_price_markup", "Spot price markup"},
	{"grid_tariff", "Grid tariff"}, {"transmission_tariff", "Transmission tariff"},
	{"system_tariff", "System tariff"}, {"electricity_tax", "Electricity tax"}, {"vat", "VAT"},
}

func discovery(cfg config.MQTT, sensor sensor) map[string]interface{} {
	result := map[string]interface{}{
		"name": sensor.name, "unique_id": cfg.ClientID + "_" + sensor.key,
		"default_entity_id": "sensor." + cfg.ClientID + "_" + sensor.key,
		"state_topic":       cfg.BaseTopic + "/state",
		"availability_mode": "all",
		"availability": []map[string]interface{}{
			{"topic": cfg.BaseTopic + "/availability"},
			{"topic": cfg.BaseTopic + "/state", "value_template": "{{ 'online' if as_timestamp(value_json.valid_from, 0) <= as_timestamp(now()) < as_timestamp(value_json.valid_until, 0) else 'offline' }}"},
		},
		"value_template":      "{{ value_json." + sensor.key + " if as_timestamp(value_json.valid_from, 0) <= as_timestamp(now()) < as_timestamp(value_json.valid_until, 0) else none }}",
		"unit_of_measurement": "DKK/kWh", "state_class": "measurement", "suggested_display_precision": 4,
		"expire_after": 3660, "qos": 1, "icon": "mdi:cash",
		"device": map[string]interface{}{"identifiers": []string{cfg.ClientID}, "name": "kwhwise", "manufacturer": "Michael Ringhus Gertz", "model": "Electricity price calculator", "sw_version": buildinfo.Version},
	}
	if sensor.key == "current_price" {
		result["json_attributes_topic"] = cfg.BaseTopic + "/state"
	}
	return result
}

func encode(snapshot application.Snapshot, rules domain.PricingRules) ([]byte, error) {
	result := priceRow(snapshot.Current)
	result["provider"], result["grid_operator"], result["customer_group"] = rules.Provider, rules.GridOperator, rules.CustomerGroup
	result["region"] = rules.Region
	result["unit"], result["resolution"], result["estimated"] = "DKK/kWh", "PT1H", true
	result["components_ore"] = snapshot.Current.Components
	prices := make([]map[string]interface{}, 0, len(snapshot.Forecast))
	for _, price := range snapshot.Forecast {
		prices = append(prices, priceRow(price))
	}
	result["prices"] = prices
	return json.Marshal(result)
}

func priceRow(price domain.PriceBreakdown) map[string]interface{} {
	result := map[string]interface{}{
		"valid_from": price.Start.UTC().Format(time.RFC3339), "valid_until": price.End.UTC().Format(time.RFC3339),
		"current_price": price.TotalInclVAT / 100, "vat": price.TotalVAT / 100, "total_excl_vat": price.TotalExclVAT / 100,
	}
	for id, component := range price.Components {
		result[id] = component.InclVAT / 100
	}
	return result
}
