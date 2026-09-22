package fec

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

func TestGF256Arithmetic(t *testing.T) {
	// Identity: a * 1 = a
	for a := 1; a < 256; a++ {
		res := gfMul(byte(a), 1)
		if res != byte(a) {
			t.Fatalf("gfMul(%d, 1) = %d, expected %d", a, res, a)
		}
	}

	// Inverse: a * inv(a) = 1 for all a != 0
	for a := 1; a < 256; a++ {
		inv := gfInv(byte(a))
		prod := gfMul(byte(a), inv)
		if prod != 1 {
			t.Fatalf("gfMul(%d, inv(%d)=%d) = %d, expected 1", a, a, inv, prod)
		}
	}

	// Division: (a * b) / b = a
	for a := 1; a < 256; a++ {
		b := byte(42)
		prod := gfMul(byte(a), b)
		quot := gfDiv(prod, b)
		if quot != byte(a) {
			t.Fatalf("gfDiv(%d, %d) = %d, expected %d", prod, b, quot, a)
		}
	}
}

func TestMatrixInversion(t *testing.T) {
	n := 4
	mat := NewMatrix(n, n)
	// Build an invertible matrix
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			mat.Set(r, c, byte((r*3+c*7+1)%255+1))
		}
	}

	inv, err := mat.Invert()
	if err != nil {
		t.Fatalf("Matrix inversion failed: %v", err)
	}

	prod, err := mat.Multiply(inv)
	if err != nil {
		t.Fatalf("Matrix multiplication failed: %v", err)
	}

	ident := NewIdentityMatrix(n)
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			if prod.Get(r, c) != ident.Get(r, c) {
				t.Fatalf("prod[%d,%d] = %d, expected %d", r, c, prod.Get(r, c), ident.Get(r, c))
			}
		}
	}
}

func TestReedSolomonMultiPacketRecovery(t *testing.T) {
	k := 6
	m := 3 // Can survive up to 3 simultaneous packet losses!

	rs, err := NewReedSolomon(k, m)
	if err != nil {
		t.Fatalf("Failed to create Reed-Solomon encoder: %v", err)
	}

	// Generate K unique test data packets
	pktLen := 128
	data := make([][]byte, k)
	for i := 0; i < k; i++ {
		data[i] = make([]byte, pktLen)
		for j := 0; j < pktLen; j++ {
			data[i][j] = byte((i*37 + j*13 + 5) % 256)
		}
	}

	// Encode to produce M parity packets
	parities, err := rs.Encode(data)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	if len(parities) != m {
		t.Fatalf("Expected %d parities, got %d", m, len(parities))
	}

	// Test 1: Single packet loss (drop data[2])
	shards := make(map[int][]byte)
	for i := 0; i < k; i++ {
		if i != 2 {
			shards[i] = data[i]
		}
	}
	shards[k] = parities[0] // Add 1 parity

	recovered, err := rs.Reconstruct(shards, pktLen)
	if err != nil {
		t.Fatalf("Reconstruct failed for 1 loss: %v", err)
	}
	if !bytes.Equal(recovered[2], data[2]) {
		t.Fatalf("Recovered packet 2 does not match original!")
	}
	t.Logf("✓ Recovered 1 lost packet correctly")

	// Test 2: Two concurrent packet losses (drop data[1] and data[4])
	shards2 := make(map[int][]byte)
	for i := 0; i < k; i++ {
		if i != 1 && i != 4 {
			shards2[i] = data[i]
		}
	}
	shards2[k] = parities[0]   // Parity 0
	shards2[k+1] = parities[1] // Parity 1

	recovered2, err := rs.Reconstruct(shards2, pktLen)
	if err != nil {
		t.Fatalf("Reconstruct failed for 2 losses: %v", err)
	}
	if !bytes.Equal(recovered2[1], data[1]) {
		t.Fatalf("Recovered packet 1 does not match original!")
	}
	if !bytes.Equal(recovered2[4], data[4]) {
		t.Fatalf("Recovered packet 4 does not match original!")
	}
	t.Logf("✓ Recovered 2 concurrent lost packets correctly")

	// Test 3: Three concurrent packet losses (drop data[0], data[3], data[5])
	shards3 := make(map[int][]byte)
	for i := 0; i < k; i++ {
		if i != 0 && i != 3 && i != 5 {
			shards3[i] = data[i]
		}
	}
	shards3[k] = parities[0]
	shards3[k+1] = parities[1]
	shards3[k+2] = parities[2]

	recovered3, err := rs.Reconstruct(shards3, pktLen)
	if err != nil {
		t.Fatalf("Reconstruct failed for 3 losses: %v", err)
	}
	for _, droppedIdx := range []int{0, 3, 5} {
		if !bytes.Equal(recovered3[droppedIdx], data[droppedIdx]) {
			t.Fatalf("Recovered packet %d does not match original!", droppedIdx)
		}
	}
	t.Logf("✓ Recovered 3 concurrent lost packets correctly (Max Distance Separable MDS limit)")
}

func TestBlockEncoderDecoderEndToEnd(t *testing.T) {
	k := 8
	m := 2
	encoder := NewBlockEncoder(k, m)
	decoder := NewBlockDecoderWithParity(k, m)

	packets := make([][]byte, k)
	for i := 0; i < k; i++ {
		packets[i] = []byte(fmt.Sprintf("Speedy-2.0-Payload-Packet-#%d-DataBytes-RandomEntropy-%04d", i, rand.Intn(1000)))
	}

	var blockID uint64
	var parities [][]byte
	for i := 0; i < k; i++ {
		bID, p, full := encoder.AddPacket(packets[i])
		if full {
			blockID = bID
			parities = p
		}
	}

	if len(parities) != m {
		t.Fatalf("Expected %d parities, got %d", m, len(parities))
	}

	// Drop packets 2 and 5!
	for i := 0; i < k; i++ {
		if i == 2 || i == 5 {
			continue // Lost in network
		}
		decoder.AddDataPacket(blockID, i, packets[i])
	}

	// Feed first parity packet
	rec1, _ := decoder.AddParityPacketWithIndex(blockID, 0, parities[0])
	if len(rec1) > 0 {
		t.Logf("Received early recovery with 1 parity: %d packets", len(rec1))
	}

	// Feed second parity packet -> should reconstruct both missing packets
	rec2, err := decoder.AddParityPacketWithIndex(blockID, 1, parities[1])
	if err != nil && len(rec1) == 0 {
		t.Fatalf("Reconstruction with 2 parities failed: %v", err)
	}

	// Total recovered across parity arrivals
	allRecovered := make(map[int][]byte)
	for idx, pkt := range rec1 {
		allRecovered[idx] = pkt
	}
	for idx, pkt := range rec2 {
		allRecovered[idx] = pkt
	}

	if len(allRecovered) < 2 {
		t.Fatalf("Expected 2 recovered packets, got %d", len(allRecovered))
	}

	if !bytes.Equal(allRecovered[2], packets[2]) {
		t.Fatalf("Recovered packet 2 mismatch!")
	}
	if !bytes.Equal(allRecovered[5], packets[5]) {
		t.Fatalf("Recovered packet 5 mismatch!")
	}
	t.Logf("✓ GATE PASS: BlockDecoder recovered 2 concurrent dropped packets (2 and 5) with M=2 Reed-Solomon")
}
