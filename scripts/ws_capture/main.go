// Command ws_capture subscribes to a FleetDeck hub's /ws/web stream and prints
// every received JSON frame as a single line to stdout. It exists so the Phase 3
// smoke script can assert which events the hub broadcast, without a browser.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	url := flag.String("url", "", "hub web socket URL (default ws://$FLEETDECK_HUB_ADDR/ws/web)")
	duration := flag.Duration("duration", 0, "capture window; 0 means run until killed")
	flag.Parse()

	if *url == "" {
		addr := os.Getenv("FLEETDECK_HUB_ADDR")
		if addr == "" {
			fmt.Fprintln(os.Stderr, "ws_capture: -url or FLEETDECK_HUB_ADDR is required")
			os.Exit(1)
		}
		*url = "ws://" + addr + "/ws/web"
	}

	conn, _, err := websocket.DefaultDialer.Dial(*url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ws_capture: dial %s: %v\n", *url, err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	var deadline time.Time
	if *duration > 0 {
		deadline = time.Now().Add(*duration)
	}
	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			return
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		fmt.Println(string(msg))
	}
}
