package mqttpublisher

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/mgertz/kwhwise/internal/application"
	"github.com/mgertz/kwhwise/internal/config"
	"github.com/mgertz/kwhwise/internal/domain"
)

type published struct {
	topic    string
	qos      byte
	retained bool
	data     interface{}
}
type fakeConnection struct {
	connected bool
	messages  []published
	afterSend func(string)
}

func (f *fakeConnection) Connect() mqtt.Token    { return completedToken{} }
func (f *fakeConnection) Disconnect(uint)        { f.connected = false }
func (f *fakeConnection) IsConnectionOpen() bool { return f.connected }
func (f *fakeConnection) Publish(topic string, qos byte, retained bool, data interface{}) mqtt.Token {
	f.messages = append(f.messages, published{topic, qos, retained, data})
	if f.afterSend != nil {
		f.afterSend(topic)
	}
	return completedToken{}
}

func TestPublisherRetriesWhenAcknowledgementCrossesPriceBoundary(t *testing.T) {
	conn := &fakeConnection{connected: true}
	p := testPublisher(conn)
	t.Cleanup(p.Close)
	conn.afterSend = func(topic string) {
		if topic == "kwhwise/state" {
			p.until = time.Now().Add(-time.Second)
		}
	}
	if err := p.Publish(context.Background(), snapshot()); err == nil {
		t.Fatal("expired send was reported successful")
	}
	if conn.messages[len(conn.messages)-1].data != "offline" {
		t.Fatal("expired send did not mark offline")
	}
	for _, message := range conn.messages {
		if message.data == "online" {
			t.Fatal("expired state was marked online")
		}
	}
}

type completedToken struct{}

func (completedToken) Wait() bool                     { return true }
func (completedToken) WaitTimeout(time.Duration) bool { return true }
func (completedToken) Error() error                   { return nil }
func (completedToken) Done() <-chan struct{}          { ch := make(chan struct{}); close(ch); return ch }

func TestPublisherRetainsPriceForSubscribersThatArriveAfterDiscovery(t *testing.T) {
	conn := &fakeConnection{connected: true}
	p := testPublisher(conn)
	t.Cleanup(p.Close)
	p.resync()
	if len(conn.messages) != len(sensors)+1 {
		t.Fatalf("unexpected discovery messages: %d", len(conn.messages))
	}
	for _, message := range conn.messages[:len(sensors)] {
		if !message.retained || message.qos != 1 {
			t.Fatalf("discovery must be retained QoS 1: %+v", message)
		}
	}
	conn.messages = nil
	if err := p.Publish(context.Background(), snapshot()); err != nil {
		t.Fatal(err)
	}
	if len(conn.messages) != 2 || !conn.messages[0].retained || !conn.messages[1].retained || conn.messages[1].data != "online" {
		t.Fatalf("unexpected state/availability behavior: %+v", conn.messages)
	}
	var state map[string]interface{}
	if err := json.Unmarshal(conn.messages[0].data.([]byte), &state); err != nil {
		t.Fatal(err)
	}
	if state["current_price"] != 1.5 || state["unit"] != "DKK/kWh" || state["provider"] != "nrgi" || state["grid_operator"] != "cerius" {
		t.Errorf("unexpected MQTT data: %+v", state)
	}
	if _, ok := state["components_ore"]; !ok {
		t.Error("missing component breakdown")
	}
	if len(state["prices"].([]interface{})) != 1 {
		t.Error("missing forecast")
	}
}

func TestDiscoveryUsesStableGenericEntityIDsAndCorrectPriceUnit(t *testing.T) {
	cfg := config.MQTT{ClientID: "kwhwise", BaseTopic: "kwhwise", DiscoveryPrefix: "homeassistant"}
	main := discovery(cfg, sensors[0])
	if main["default_entity_id"] != "sensor.kwhwise_current_price" || main["unit_of_measurement"] != "DKK/kWh" || main["unique_id"] != "kwhwise_current_price" {
		t.Fatalf("invalid discovery: %+v", main)
	}
	if _, set := main["device_class"]; set {
		t.Error("price per kWh must not be advertised as a plain monetary amount")
	}
	if _, set := main["availability_topic"]; set {
		t.Fatal("single availability_topic must not coexist with the availability list")
	}
	if main["availability_mode"] != "all" {
		t.Fatal("connection and timestamp validity must both be required")
	}
	availability := main["availability"].([]map[string]interface{})
	if len(availability) != 2 || availability[1]["topic"] != "kwhwise/state" {
		t.Fatal("missing retained-state validity check")
	}
	if _, set := discovery(cfg, sensors[1])["json_attributes_topic"]; set {
		t.Error("forecast should not be duplicated across component sensors")
	}
}

func TestPublisherReplaysSavedPriceOnReconnectButNotAfterExpiry(t *testing.T) {
	conn := &fakeConnection{}
	p := testPublisher(conn)
	t.Cleanup(p.Close)
	if err := p.Publish(context.Background(), snapshot()); err == nil {
		t.Fatal("expected disconnected error")
	}
	conn.connected = true
	p.resync()
	stateMessages := 0
	for _, message := range conn.messages {
		if message.topic == "kwhwise/state" {
			stateMessages++
		}
	}
	if stateMessages != 1 {
		t.Fatalf("reconnect state messages = %d, want 1", stateMessages)
	}
	conn.messages = nil
	p.until = time.Now().Add(-time.Second)
	p.resync()
	for _, message := range conn.messages {
		if message.topic == "kwhwise/state" {
			t.Fatal("replayed an expired price")
		}
	}
	if conn.messages[len(conn.messages)-1].data != "offline" {
		t.Fatal("expired price was not marked unavailable")
	}
}

func TestExpiryMarksUnavailableAndIgnoresAnOldTimerAfterPriceUpdate(t *testing.T) {
	conn := &fakeConnection{connected: true}
	p := testPublisher(conn)
	t.Cleanup(p.Close)
	if err := p.Publish(context.Background(), snapshot()); err != nil {
		t.Fatal(err)
	}
	oldDeadline := p.until
	newSnapshot := snapshot()
	newSnapshot.Current.End = oldDeadline.Add(time.Hour)
	if err := p.Publish(context.Background(), newSnapshot); err != nil {
		t.Fatal(err)
	}
	conn.messages = nil
	p.expireAt(oldDeadline)
	if len(conn.messages) != 0 {
		t.Fatal("old timer invalidated the new price")
	}
	p.until = time.Now().Add(-time.Second)
	p.expireAt(p.until)
	if len(conn.messages) != 1 || conn.messages[0].data != "offline" || !conn.messages[0].retained {
		t.Fatalf("expiry must publish retained offline: %+v", conn.messages)
	}
}

func TestUnavailableClearsReconnectStateAndCloseMarksOffline(t *testing.T) {
	conn := &fakeConnection{connected: true}
	p := testPublisher(conn)
	if err := p.Publish(context.Background(), snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := p.Unavailable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.latest != nil {
		t.Fatal("invalidated price remains cached")
	}
	p.Close()
	if !p.closed || conn.connected || conn.messages[len(conn.messages)-1].data != "offline" {
		t.Fatal("publisher did not close cleanly")
	}
}

func TestPublisherRejectsExpiredPrice(t *testing.T) {
	p := testPublisher(&fakeConnection{connected: true})
	s := snapshot()
	s.Current.End = time.Now().Add(-time.Second)
	if err := p.Publish(context.Background(), s); err == nil {
		t.Fatal("accepted expired price")
	}
}

func testPublisher(conn *fakeConnection) *Publisher {
	return &Publisher{client: conn, cfg: config.MQTT{ClientID: "kwhwise", BaseTopic: "kwhwise", DiscoveryPrefix: "homeassistant"},
		rules: domain.PricingRules{Provider: "nrgi", GridOperator: "cerius", CustomerGroup: "C"}, logger: log.New(io.Discard, "", 0)}
}

func snapshot() application.Snapshot {
	now := time.Now().UTC()
	price := domain.PriceBreakdown{Start: now.Add(-time.Minute), End: now.Add(time.Hour), TotalInclVAT: 150, TotalExclVAT: 120, TotalVAT: 30,
		Components: map[string]domain.Component{"spot_price": {ExclVAT: 64, VAT: 16, InclVAT: 80}}}
	return application.Snapshot{Current: price, Forecast: []domain.PriceBreakdown{price}}
}
