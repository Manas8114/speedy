package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"speedy/pkg/crypto"
	"speedy/pkg/engine"
)

func main() {
	listenAddr := flag.String("listen", ":51820", "UDP listen address for relay")
	poolCIDR := flag.String("pool", "10.254.1.0/24", "Tunnel address pool CIDR")
	tunName := flag.String("tun", "speedy-relay0", "TUN device name")
	natIface := flag.String("nat-iface", "eth0", "Egress physical interface for NAT masquerade")
	keyFile := flag.String("key-file", "relay_key.hex", "Path to relay static private key file")
	flag.Parse()

	log.Printf("Starting Speedy 2.0 Relay on %s (pool: %s, tun: %s)...", *listenAddr, *poolCIDR, *tunName)

	var keyPair crypto.KeyPair
	if data, err := os.ReadFile(*keyFile); err == nil {
		k, err := crypto.KeyFromHex(string(data))
		if err == nil {
			keyPair.Private = k
			// Derive pub
			kp, _ := crypto.GenerateKeyPair()
			keyPair.Public = kp.Public
		}
	}

	if keyPair.Private == (crypto.Key{}) {
		kp, err := crypto.GenerateKeyPair()
		if err != nil {
			log.Fatalf("Failed to generate relay key pair: %v", err)
		}
		keyPair = kp
		_ = os.WriteFile(*keyFile, []byte(kp.Private.String()), 0600)
		log.Printf("Generated new relay static key pair.")
	}

	log.Printf("Relay Public Key: %s", keyPair.Public.String())

	cfg := engine.RelayConfig{
		ListenAddr: *listenAddr,
		PoolCIDR:   *poolCIDR,
		TunName:    *tunName,
		StaticKey:  keyPair,
		NatIface:   *natIface,
	}

	r, err := engine.NewRelayEngine(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize relay engine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		log.Fatalf("Relay start failed: %v", err)
	}

	log.Println("Speedy 2.0 Relay is ACTIVE and accepting client paths.")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down relay...")
	_ = r.Stop()
	fmt.Println("Relay shutdown complete.")
}
