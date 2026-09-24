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

	"path/filepath"
	"speedy/pkg/crypto"
	"speedy/pkg/engine"
	"speedy/pkg/plexo"
	"speedy/pkg/routing"
	"speedy/pkg/service"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "download" {
		runDownloadSubcommand(os.Args[2:])
		return
	}

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

func runDownloadSubcommand(args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	urlFlag := fs.String("url", "", "URL of the file to download")
	outFlag := fs.String("out", "", "Output destination file path")
	chunkFlag := fs.Int("chunk", 4, "Chunk size in MB")
	_ = fs.Parse(args)

	targetURL := *urlFlag
	if targetURL == "" && fs.NArg() > 0 {
		targetURL = fs.Arg(0)
	}
	if targetURL == "" {
		log.Fatal("Error: please provide a download URL via --url or as an argument")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fmt.Printf("🔍 Probing URL: %s...\n", targetURL)
	probe, err := plexo.ProbeURL(ctx, targetURL)
	if err != nil {
		log.Fatalf("Probe failed: %v", err)
	}

	fmt.Printf("✓ File: %s (Size: %d bytes, Range 206: %t)\n", probe.Filename, probe.TotalBytes, probe.SupportsRanges)

	ifaces, err := plexo.AvailableInterfaces()
	if err != nil || len(ifaces) == 0 {
		log.Fatalf("No available network interfaces: %v", err)
	}

	destPath := *outFlag
	if destPath == "" {
		home, _ := os.UserHomeDir()
		dl := filepath.Join(home, "Downloads")
		if stat, err := os.Stat(dl); err == nil && stat.IsDir() {
			destPath = filepath.Join(dl, probe.Filename)
		} else {
			destPath = probe.Filename
		}
	}

	sess, err := plexo.NewSession(probe, destPath, ifaces, int64(*chunkFlag)*1024*1024)
	if err != nil {
		log.Fatalf("Creating session failed: %v", err)
	}

	fmt.Printf("🚀 Starting Plexo Turbo download across %d interface(s) -> %s\n", len(ifaces), destPath)
	if err := sess.Start(); err != nil {
		log.Fatalf("Start failed: %v", err)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		st := sess.Stats()
		pct := 0.0
		if st.TotalBytes > 0 {
			pct = float64(st.CompletedBytes) / float64(st.TotalBytes) * 100.0
		}
		speedMB := float64(st.SpeedBps) / 1_000_000.0
		fmt.Printf("\r⬇️  Progress: %5.1f%% (%d / %d bytes) | Speed: %5.2f MB/s", pct, st.CompletedBytes, st.TotalBytes, speedMB)
		if st.State == "done" {
			fmt.Printf("\n✓ Download completed successfully: %s\n", destPath)
			return
		}
		if st.State == "cancelled" {
			fmt.Printf("\n✗ Download cancelled.\n")
			return
		}
	}
}

