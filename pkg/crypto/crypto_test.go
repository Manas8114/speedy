package crypto

import (
	"bytes"
	"testing"
)

func TestNoiseIKHandshake(t *testing.T) {
	// Generate Relay static key pair
	relayKey, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("Generate relay key failed: %v", err)
	}

	// Generate Client static key pair
	clientKey, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("Generate client key failed: %v", err)
	}

	// Client initializes handshake knowing relay public key
	initiator, err := NewInitiator(clientKey, relayKey.Public)
	if err != nil {
		t.Fatalf("NewInitiator failed: %v", err)
	}

	clientPayload := []byte("Speedy-v2.0-client-init-metadata")
	initMsg, err := initiator.CreateInitMessage(clientPayload)
	if err != nil {
		t.Fatalf("CreateInitMessage failed: %v", err)
	}

	// Relay receives and processes init message
	responder := NewResponder(relayKey)
	relayPayload := []byte("Speedy-v2.0-assigned-ip=10.254.1.2/24")

	relaySession, authClientPub, decClientPayload, respMsg, err := responder.ProcessInitMessage(initMsg, relayPayload)
	if err != nil {
		t.Fatalf("ProcessInitMessage failed: %v", err)
	}

	if authClientPub != clientKey.Public {
		t.Fatalf("Relay authenticated client public key mismatch: got %v, want %v", authClientPub, clientKey.Public)
	}

	if !bytes.Equal(decClientPayload, clientPayload) {
		t.Fatalf("Relay decrypted client payload mismatch: got %s, want %s", decClientPayload, clientPayload)
	}

	// Client processes relay response
	clientSession, decRelayPayload, err := initiator.ProcessRespMessage(respMsg)
	if err != nil {
		t.Fatalf("ProcessRespMessage failed: %v", err)
	}

	if !bytes.Equal(decRelayPayload, relayPayload) {
		t.Fatalf("Client decrypted relay payload mismatch: got %s, want %s", decRelayPayload, relayPayload)
	}

	// Test bidirectional transport data encryption
	// Client -> Relay
	clientPlaintext := []byte("GET /speedy HTTP/1.1\r\nHost: example.com\r\n\r\n")
	ad := []byte("header-ad-path-0")
	ciphertext := clientSession.TxState.Encrypt(nil, 0, 1, clientPlaintext, ad)

	decryptedByRelay, err := relaySession.RxState.Decrypt(nil, 0, 1, ciphertext, ad)
	if err != nil {
		t.Fatalf("Relay failed to decrypt client data: %v", err)
	}
	if !bytes.Equal(decryptedByRelay, clientPlaintext) {
		t.Fatalf("Relay decrypted data mismatch: got %s, want %s", decryptedByRelay, clientPlaintext)
	}

	// Relay -> Client
	relayPlaintext := []byte("HTTP/1.1 200 OK\r\nContent-Length: 12\r\n\r\nSpeedy Bonds")
	replyCiphertext := relaySession.TxState.Encrypt(nil, 0, 1, relayPlaintext, ad)

	decryptedByClient, err := clientSession.RxState.Decrypt(nil, 0, 1, replyCiphertext, ad)
	if err != nil {
		t.Fatalf("Client failed to decrypt relay reply: %v", err)
	}
	if !bytes.Equal(decryptedByClient, relayPlaintext) {
		t.Fatalf("Client decrypted reply mismatch: got %s, want %s", decryptedByClient, relayPlaintext)
	}

	// Tampered ciphertext must fail authentication
	tamperedCiphertext := make([]byte, len(ciphertext))
	copy(tamperedCiphertext, ciphertext)
	tamperedCiphertext[len(tamperedCiphertext)-1] ^= 0xFF

	_, err = relaySession.RxState.Decrypt(nil, 0, 1, tamperedCiphertext, ad)
	if err == nil {
		t.Fatal("expected authentication error on tampered ciphertext, got nil")
	}
}

func TestReplayFilter(t *testing.T) {
	rf := NewReplayFilter()

	// In-order packets
	for i := uint64(1); i <= 10; i++ {
		if !rf.CheckAndSet(i) {
			t.Fatalf("expected packet %d to be accepted", i)
		}
	}

	// Replay duplicate
	if rf.CheckAndSet(5) {
		t.Fatal("expected replayed packet 5 to be rejected")
	}
	if rf.CheckAndSet(10) {
		t.Fatal("expected duplicate packet 10 to be rejected")
	}

	// Sequence jump within window
	if !rf.CheckAndSet(25) {
		t.Fatal("expected packet 25 to be accepted")
	}

	// Out of order packet within window
	if !rf.CheckAndSet(20) {
		t.Fatal("expected out of order packet 20 to be accepted")
	}
	// Duplicate of that out of order packet
	if rf.CheckAndSet(20) {
		t.Fatal("expected duplicate packet 20 to be rejected")
	}

	// Large sequence jump
	if !rf.CheckAndSet(150) {
		t.Fatal("expected packet 150 to be accepted")
	}

	// Packets older than window (150 - 64 = 86)
	if rf.CheckAndSet(25) {
		t.Fatal("expected old packet 25 to be rejected")
	}
}
