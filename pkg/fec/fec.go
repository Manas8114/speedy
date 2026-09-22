package fec

import (
	"errors"
	"sync"
)

// BlockEncoder generates parity packets for a block of K data packets using Reed-Solomon GF(2^8)
type BlockEncoder struct {
	mu        sync.Mutex
	k         int // Data packets per block (e.g. 10)
	m         int // Parity packets per block (e.g. 1, 2, or more)
	rs        *ReedSolomon
	blockID   uint64
	buffer    [][]byte
	maxPktLen int
}

func NewBlockEncoder(k, m int) *BlockEncoder {
	if k <= 0 {
		k = 10
	}
	if m < 0 {
		m = 1
	}
	var rs *ReedSolomon
	if m > 0 {
		rs, _ = NewReedSolomon(k, m)
	}
	return &BlockEncoder{
		k:      k,
		m:      m,
		rs:     rs,
		buffer: make([][]byte, 0, k),
	}
}

// AddPacket adds a data packet to the current FEC block.
// When the block reaches K packets, it returns M parity packets generated from the block.
func (e *BlockEncoder) AddPacket(pkt []byte) (blockID uint64, parities [][]byte, isBlockFull bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cp := make([]byte, len(pkt))
	copy(cp, pkt)
	e.buffer = append(e.buffer, cp)
	if len(pkt) > e.maxPktLen {
		e.maxPktLen = len(pkt)
	}

	if len(e.buffer) < e.k {
		return e.blockID, nil, false
	}

	// Block is full: generate parity packets
	e.blockID++
	bID := e.blockID

	if e.m > 0 {
		if e.rs != nil {
			var err error
			parities, err = e.rs.Encode(e.buffer)
			if err != nil {
				// Fallback to XOR
				parities = e.generateXORParity()
			}
		} else {
			parities = e.generateXORParity()
		}
	}

	// Reset block buffer
	e.buffer = make([][]byte, 0, e.k)
	e.maxPktLen = 0

	return bID, parities, true
}

func (e *BlockEncoder) generateXORParity() [][]byte {
	parity := make([]byte, e.maxPktLen)
	for _, p := range e.buffer {
		for i := 0; i < len(p); i++ {
			parity[i] ^= p[i]
		}
	}
	return [][]byte{parity}
}

// BlockDecoder reconstructs missing data packets using Reed-Solomon GF(2^8) parity
type BlockDecoder struct {
	mu       sync.Mutex
	k        int
	m        int
	rs       *ReedSolomon
	blocks   map[uint64]*decodingBlock
	maxSlots int
}

type decodingBlock struct {
	dataPackets   map[int][]byte // index in block (0..k-1) -> packet data
	parityPackets map[int][]byte // parity index (0..m-1) -> packet data
	maxPktLen     int
}

func NewBlockDecoder(k int) *BlockDecoder {
	return NewBlockDecoderWithParity(k, 1)
}

func NewBlockDecoderWithParity(k, m int) *BlockDecoder {
	if k <= 0 {
		k = 10
	}
	if m <= 0 {
		m = 1
	}
	rs, _ := NewReedSolomon(k, m)
	return &BlockDecoder{
		k:        k,
		m:        m,
		rs:       rs,
		blocks:   make(map[uint64]*decodingBlock),
		maxSlots: 100,
	}
}

// AddDataPacket adds a received data packet
func (d *BlockDecoder) AddDataPacket(blockID uint64, indexInBlock int, pkt []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()

	blk, exists := d.blocks[blockID]
	if !exists {
		blk = &decodingBlock{
			dataPackets:   make(map[int][]byte),
			parityPackets: make(map[int][]byte),
		}
		d.blocks[blockID] = blk
	}

	cp := make([]byte, len(pkt))
	copy(cp, pkt)
	blk.dataPackets[indexInBlock] = cp
	if len(pkt) > blk.maxPktLen {
		blk.maxPktLen = len(pkt)
	}
}

// AddParityPacket adds a received parity packet and attempts reconstruction if 1 packet is missing (backward compatible)
func (d *BlockDecoder) AddParityPacket(blockID uint64, parity []byte) ([]byte, int, error) {
	recovered, err := d.AddParityPacketWithIndex(blockID, 0, parity)
	if err != nil {
		return nil, -1, err
	}
	for idx, pkt := range recovered {
		return pkt, idx, nil
	}
	return nil, -1, errors.New("no packet needed reconstruction")
}

// AddParityPacketWithIndex adds a parity packet by index (0..M-1) and reconstructs all recoverable data packets
func (d *BlockDecoder) AddParityPacketWithIndex(blockID uint64, parityIdx int, parity []byte) (map[int][]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	blk, exists := d.blocks[blockID]
	if !exists {
		blk = &decodingBlock{
			dataPackets:   make(map[int][]byte),
			parityPackets: make(map[int][]byte),
		}
		d.blocks[blockID] = blk
	}

	cp := make([]byte, len(parity))
	copy(cp, parity)
	blk.parityPackets[parityIdx] = cp
	if len(parity) > blk.maxPktLen {
		blk.maxPktLen = len(parity)
	}

	// Check how many data packets are missing
	missingCount := d.k - len(blk.dataPackets)
	if missingCount <= 0 {
		return nil, nil // All data packets already arrived
	}

	// If total received shards (data + parity) >= K, we can reconstruct!
	totalReceived := len(blk.dataPackets) + len(blk.parityPackets)
	if totalReceived >= d.k {
		// Prepare received shards map for Reed-Solomon engine
		shards := make(map[int][]byte, totalReceived)
		for idx, data := range blk.dataPackets {
			shards[idx] = data
		}
		for pIdx, pData := range blk.parityPackets {
			shards[d.k+pIdx] = pData
		}

		if d.rs != nil {
			recoveredMap, err := d.rs.Reconstruct(shards, blk.maxPktLen)
			if err == nil {
				newlyRecovered := make(map[int][]byte)
				for idx, pkt := range recoveredMap {
					if _, alreadyHad := blk.dataPackets[idx]; !alreadyHad {
						blk.dataPackets[idx] = pkt
						newlyRecovered[idx] = pkt
					}
				}
				if len(newlyRecovered) > 0 {
					return newlyRecovered, nil
				}
			}
		}

		// Fallback for M=1 XOR reconstruction if Reed-Solomon engine is nil
		if d.rs == nil && missingCount == 1 && len(blk.parityPackets) >= 1 {
			missingIdx := -1
			for i := 0; i < d.k; i++ {
				if _, ok := blk.dataPackets[i]; !ok {
					missingIdx = i
					break
				}
			}
			if missingIdx != -1 {
				reconstructed := make([]byte, blk.maxPktLen)
				for _, p := range blk.parityPackets {
					copy(reconstructed, p)
					break
				}
				for _, p := range blk.dataPackets {
					for j := 0; j < len(p); j++ {
						reconstructed[j] ^= p[j]
					}
				}
				blk.dataPackets[missingIdx] = reconstructed
				return map[int][]byte{missingIdx: reconstructed}, nil
			}
		}
	}

	return nil, errors.New("cannot reconstruct: insufficient parity or missing packets")
}

// CleanOldBlocks flushes state older than maxSlots
func (d *BlockDecoder) CleanOldBlocks(currentBlockID uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for bID := range d.blocks {
		if bID+uint64(d.maxSlots) < currentBlockID {
			delete(d.blocks, bID)
		}
	}
}
