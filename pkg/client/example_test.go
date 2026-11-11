package client_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/client"
)

// Connect over TCP with an API key read from the environment. Keys are
// sent only in the Authorization header.
func ExampleNewTCP() {
	c, err := client.NewTCP("http://127.0.0.1:7878", os.Getenv("COCKPIT_API_KEY"), client.WithTimeout(5*time.Second))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	h, err := c.Health(context.Background())
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			log.Fatalf("%s: %s (request %s)", apiErr.Code, apiErr.Message, apiErr.RequestID)
		}
		log.Fatal(err)
	}
	fmt.Println("health:", h.Status)
}

// The local admin socket needs no key and may manage keys and settings.
func ExampleNewUnix() {
	c, err := client.NewUnix(os.ExpandEnv("$XDG_RUNTIME_DIR/cockpit-monitor/agent.sock"))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	keys, err := c.ListKeys(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	for _, k := range keys {
		fmt.Println(k.ID, k.Name, k.Scopes)
	}
}

// Every section reports its availability; check it before using data.
func ExampleClient_Metrics() {
	c, err := client.NewTCP("http://127.0.0.1:7878", os.Getenv("COCKPIT_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	m, err := c.Metrics(context.Background(), "cpu", "memory")
	if err != nil {
		log.Fatal(err)
	}
	if cpu := m.CPU; cpu.Status == "available" && cpu.Data != nil && cpu.Data.UsagePercent != nil {
		fmt.Printf("cpu %.1f%%\n", *cpu.Data.UsagePercent)
	} else {
		fmt.Println("cpu unavailable:", cpu.Reason)
	}
}

// Stored history: the agent picks the resolution for the range.
func ExampleClient_History() {
	c, err := client.NewTCP("http://127.0.0.1:7878", os.Getenv("COCKPIT_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	now := time.Now()
	h, err := c.History(context.Background(), client.HistoryQuery{Series: []string{"cpu.usage_percent"}, From: now.Add(-24 * time.Hour), To: now})
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range h.Series {
		fmt.Println(s.Name, h.Resolution, len(s.Points), "points")
	}
}

// Live events until the context ends. Lagged reports events dropped
// because the client read too slowly.
func ExampleClient_StreamEvents() {
	c, err := client.NewTCP("http://127.0.0.1:7878", os.Getenv("COCKPIT_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err = c.StreamEvents(ctx, "warning", func(m client.StreamMessage) error {
		if m.Lagged > 0 {
			fmt.Println("missed", m.Lagged, "events")
			return nil
		}
		fmt.Println(m.Event.Time, m.Event.Severity, m.Event.Message)
		return nil
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		log.Fatal(err)
	}
}
