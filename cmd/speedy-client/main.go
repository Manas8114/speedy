package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"speedy/pkg/crypto"
	"speedy/pkg/engine"
	"speedy/pkg/routing"
	"speedy/pkg/service"
)

func main() {
	serviceCmd := flag.String("service", "", "Windows service command: install, uninstall, start, stop, status, run")
	relayAddr := flag.String("relay", "127.0.0.1:51820", "Relay host:port")
	relayPubHex := flag.String("relay-pubkey", "", "Relay static public key hex")
	tunName := flag.String("tun", "speedy-client0", "TUN device name")
	defaultRoute := flag.Bool("default-route", false, "Install default route with routing-loop guard")
	multipathMode := flag.Bool("multipath", true, "Enable multipath bonding across physical uplinks")
	selectedTier := flag.Int("tier", 2, "Scheduler tier (1: RR, 2: Goodput, 3: MinRTT, 4: HoL, 0: Redundant)")
	enableFEC := flag.Bool("fec", true, "Enable Forward Error Correction parity packets")
	flag.Parse()

	if *serviceCmd != "" {
		switch *serviceCmd {
		case "run":
			if err := service.RunService(); err != nil {
				log.Fatalf("Failed to run service: %v", err)
			}
			return
		case "install":
			exe, _ := os.Executable()
			if err := service.InstallService(exe); err != nil {
				log.Fatalf("Failed to install service: %v", err)
			}
			fmt.Println("✓ Service 'speedy-tunnel' installed successfully.")
			return
		case "uninstall":
			if err := service.UninstallService(); err != nil {
				log.Fatalf("Failed to uninstall service: %v", err)
			}
			fmt.Println("✓ Service 'speedy-tunnel' uninstalled successfully.")
			return
		case "start":
			if err := service.StartService(); err != nil {
				log.Fatalf("Failed to start service: %v", err)
			}
			fmt.Println("✓ Service 'speedy-tunnel' started.")
			return
		case "stop":
			if err := service.StopService(); err != nil {
				log.Fatalf("Failed to stop service: %v", err)
			}
			fmt.Println("✓ Service 'speedy-tunnel' stopped.")
			return
		case "status":
			st, err := service.GetServiceStatus()
			fmt.Printf("Service 'speedy-tunnel' Status: %s (Err: %v)\n", st, err)
			return
		default:
			log.Fatalf("Unknown service action: %s (supported: install, uninstall, start, stop, status, run)", *serviceCmd)
		}
	}

	if *relayPubHex == "" {
		log.Fatal("Error: -relay-pubkey is required")
	}

	log.Printf("Connecting to Speedy 2.0 Relay at %s (Multipath: %t)...", *relayAddr, *multipathMode)

	clientKey, err := crypto.GenerateKeyPair()
	if err != nil {
		log.Fatalf("Failed to generate client key: %v", err)
	}

	routeMgr := routing.NewManager(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stopFunc func() error
	var statsFunc func() (uint64, uint64, uint64, uint64)

	if *multipathMode {
		mpCfg := engine.MultipathConfig{
			RelayAddr:       *relayAddr,
			RelayPubKeyHex:  *relayPubHex,
			TunName:         *tunName,
			InstallDefRoute: *defaultRoute,
			RouteManager:    routeMgr,
			StaticKey:       clientKey,
			SelectedTier:    *selectedTier,
			EnableFEC:       *enableFEC,
		}

		mpClient, err := engine.NewMultipathEngine(mpCfg)
		if err != nil {
			log.Fatalf("Multipath engine init failed: %v", err)
		}

		if err := mpClient.Start(ctx); err != nil {
			log.Fatalf("Multipath client connection failed: %v", err)
		}

		log.Printf("Speedy 2.0 Multipath Tunnel ESTABLISHED! Session ID: %d, Assigned IP: %s", mpClient.SessionID(), mpClient.AssignedIP())
		stopFunc = mpClient.Stop
		statsFunc = mpClient.Stats

		// Status logger ticker with diagnostics
		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				diag := mpClient.Diagnostics()
				sentB, recvB, sentP, recvP := statsFunc()
				log.Printf("[MULTIPATH] Agg Goodput: %.1f Mbps | Active Paths: %d | Buffer Pkts: %d | Sent: %d pkts (%d B), Recv: %d pkts (%d B)",
					diag.AggregateThroughput/(1024*1024), len(diag.Paths), diag.ReorderBuffer.Occupancy, sentP, sentB, recvP, recvB)
			}
		}()
	} else {
		cfg := engine.ClientConfig{
			RelayAddr:       *relayAddr,
			RelayPubKeyHex:  *relayPubHex,
			TunName:         *tunName,
			InstallDefRoute: *defaultRoute,
			RouteManager:    routeMgr,
			StaticKey:       clientKey,
		}

		client, err := engine.NewClientEngine(cfg)
		if err != nil {
			log.Fatalf("Client engine init failed: %v", err)
		}

		if err := client.Start(ctx); err != nil {
			log.Fatalf("Client connection failed: %v", err)
		}

		log.Printf("Speedy 2.0 Single-Path Tunnel ESTABLISHED! Session ID: %d, Assigned IP: %s", client.SessionID(), client.AssignedIP())
		stopFunc = client.Stop
		statsFunc = client.Stats

		// Status logger ticker
		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				sentB, recvB, sentP, recvP := statsFunc()
				log.Printf("[TELEMETRY] Sent: %d pkts (%d B), Recv: %d pkts (%d B)", sentP, sentB, recvP, recvB)
			}
		}()
	}

	if *defaultRoute {
		log.Println("Default route active (Relay pinned to physical uplink gateway).")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down Speedy client...")
	_ = stopFunc()
	fmt.Println("Tunnel closed.")
}
