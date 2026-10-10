//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// This test uses a pre-built kwhwise:local image, a disposable broker/network,
// and the real public Energinet API. It does not touch an existing MQTT stack.
func TestContainerWithRealBroker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is required")
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	network, broker, app := "kwhwise_test_net_"+suffix, "kwhwise_test_broker_"+suffix, "kwhwise_test_app_"+suffix
	docker(t, "network", "create", network)
	t.Cleanup(func() { cleanup("network", "rm", network) })
	configuration := filepath.Join(t.TempDir(), "mosquitto.conf")
	if err := os.WriteFile(configuration, []byte("listener 1883\nallow_anonymous true\npersistence false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Docker may change an automatically assigned host port on restart. Pick
	// an unused explicit port so this test exercises a stable broker address.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	docker(t, "run", "-d", "--name", broker, "--network", network, "-p", "127.0.0.1:"+port+":1883",
		"-v", configuration+":/mosquitto/config/mosquitto.conf:ro,Z", "eclipse-mosquitto:2.0")
	t.Cleanup(func() { cleanup("rm", "-f", broker) })
	address := strings.TrimSpace(docker(t, "port", broker, "1883/tcp"))
	type packet struct {
		topic string
		data  []byte
	}
	messages := make(chan packet, 128)
	ready := make(chan struct{}, 8)
	options := mqtt.NewClientOptions().AddBroker("tcp://" + address).SetClientID("kwhwise_observer_" + suffix).
		SetAutoReconnect(true).SetConnectRetry(true).SetConnectRetryInterval(time.Second).SetOrderMatters(false)
	options.SetOnConnectHandler(func(client mqtt.Client) {
		token := client.SubscribeMultiple(map[string]byte{"homeassistant/sensor/kwhwise_test/#": 1, "kwhwise_test/#": 1}, func(_ mqtt.Client, message mqtt.Message) {
			data := append([]byte(nil), message.Payload()...)
			select {
			case messages <- packet{message.Topic(), data}:
			default:
			}
		})
		if token.WaitTimeout(5*time.Second) && token.Error() == nil {
			ready <- struct{}{}
		}
	})
	observer := mqtt.NewClient(options)
	observer.Connect()
	t.Cleanup(func() { observer.Disconnect(250) })
	awaitReady := func() {
		t.Helper()
		select {
		case <-ready:
		case <-time.After(15 * time.Second):
			t.Fatal("observer did not connect/subscribe")
		}
	}
	awaitReady()
	started := time.Now()
	docker(t, "run", "-d", "--name", app, "--network", network, "--read-only", "--memory=64m", "--cpus=1",
		"-e", "ELECTRICITY_PROVIDER=nrgi", "-e", "REGION=dk2", "-e", "GRID_OPERATOR=cerius", "-e", "SPOT_PRICE_MARKUP_ORE=9",
		"-e", "MQTT_HOST="+broker, "-e", "MQTT_CLIENT_ID=kwhwise_test", "-e", "MQTT_BASE_TOPIC=kwhwise_test", "kwhwise:local")
	t.Cleanup(func() { cleanup("rm", "-f", app) })
	state := func() map[string]interface{} {
		t.Helper()
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case packet := <-messages:
				if strings.HasSuffix(packet.topic, "/current_price/config") {
					var discovery map[string]interface{}
					if err := json.Unmarshal(packet.data, &discovery); err != nil {
						t.Fatal(err)
					}
					if discovery["default_entity_id"] != "sensor.kwhwise_test_current_price" || discovery["unit_of_measurement"] != "DKK/kWh" {
						t.Fatalf("invalid discovery: %v", discovery)
					}
				}
				if packet.topic == "kwhwise_test/state" {
					var result map[string]interface{}
					if err := json.Unmarshal(packet.data, &result); err != nil {
						t.Fatal(err)
					}
					return result
				}
			case <-deadline.C:
				t.Log(docker(t, "logs", app))
				t.Fatal("no valid price received")
			}
		}
	}
	initial := state()
	t.Logf("Container startup to first MQTT price: %s", time.Since(started))
	if initial["provider"] != "nrgi" || initial["grid_operator"] != "cerius" || initial["region"] != "DK2" {
		t.Fatalf("wrong price metadata: %v", initial)
	}
	prices := initial["prices"].([]interface{})
	if len(prices) < 23 || len(prices) > 25 {
		t.Fatalf("unexpected forecast length %d", len(prices))
	}
	components := initial["components_ore"].(map[string]interface{})
	var total float64
	for _, value := range components {
		total += value.(map[string]interface{})["incl_vat_ore"].(float64) / 100
	}
	if math.Abs(total-initial["current_price"].(float64)) > 1e-9 {
		t.Fatalf("components do not sum to total: %v", initial)
	}
	if initial["spot_price_markup"] != 0.09 || initial["system_tariff"] != 0.09 || initial["electricity_tax"] != 0.01 {
		t.Fatalf("wrong markup/national charges: %v", initial)
	}
	// Model HA's asynchronous discovery: the entity starts subscribing only
	// after initial discovery AND state have already been published.
	cmd := exec.Command("docker", "exec", broker, "mosquitto_sub", "-h", "127.0.0.1", "-t", "kwhwise_test/state", "-C", "1", "-W", "5")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("late subscriber did not receive price: %s (%v)", output, err)
	}
	var lateState map[string]interface{}
	if err := json.Unmarshal(output, &lateState); err != nil {
		t.Fatal(err)
	}
	if lateState["current_price"] != initial["current_price"] {
		t.Fatal("late subscriber received a different price")
	}
	drain := func() {
		for len(messages) > 0 {
			<-messages
		}
	}
	birth := func() {
		t.Helper()
		drain()
		token := observer.Publish("homeassistant/status", 1, true, "online")
		if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
			t.Fatal("could not send HA birth message")
		}
	}
	birth()
	if replayed := state(); replayed["current_price"] != initial["current_price"] {
		t.Fatal("HA restart did not replay valid cached price")
	}
	for len(ready) > 0 {
		<-ready
	}
	docker(t, "restart", broker)
	awaitReady()
	birth()
	if replayed := state(); replayed["provider"] != "nrgi" {
		t.Fatal("broker reconnect did not replay state")
	}
	t.Log(docker(t, "stats", "--no-stream", "--format", "{{.MemUsage}} CPU={{.CPUPerc}}", app))
	t.Log("Image size bytes: " + docker(t, "image", "inspect", "--format", "{{.Size}}", "kwhwise:local"))
	drain()
	docker(t, "stop", "-t", "10", app)
	if strings.TrimSpace(docker(t, "inspect", "--format", "{{.State.ExitCode}}", app)) != "0" {
		t.Fatal("container did not exit cleanly")
	}
	offline := false
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !offline {
		select {
		case packet := <-messages:
			offline = packet.topic == "kwhwise_test/availability" && string(packet.data) == "offline"
		case <-deadline.C:
			t.Fatal("shutdown did not publish offline")
		}
	}
	t.Log("Verified discovery, live total price, forecast, HA restart, broker reconnect and graceful shutdown")
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %s (%v)", args, output, err)
	}
	return strings.TrimSpace(string(output))
}

func cleanup(args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", args...).Run()
}
