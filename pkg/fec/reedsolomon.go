package fec

import (
	"errors"
	"fmt"
)

var (
	ErrNotEnoughShards = errors.New("not enough shards received to reconstruct block")
	ErrTooManyShards   = errors.New("too many shards provided")
)

// ReedSolomon provides systematic (K, M) erasure coding over GF(2^8) using Cauchy matrices.
type ReedSolomon struct {
	k         int     // Number of data shards
	m         int     // Number of parity shards
	generator *Matrix // (K+M) x K systematic Cauchy generator matrix
}

// NewReedSolomon creates a new Reed-Solomon encoder/decoder instance
func NewReedSolomon(k, m int) (*ReedSolomon, error) {
	if k <= 0 || m <= 0 {
		return nil, fmt.Errorf("k and m must be positive, got k=%d, m=%d", k, m)
	}
	gen, err := BuildCauchyMatrix(k, m)
	if err != nil {
		return nil, err
	}
	return &ReedSolomon{
		k:         k,
		m:         m,
		generator: gen,
	}, nil
}

// DataShards returns K
func (rs *ReedSolomon) DataShards() int {
	return rs.k
}

// ParityShards returns M
func (rs *ReedSolomon) ParityShards() int {
	return rs.m
}

// Encode computes M parity shards from K data shards.
// All shards are zero-padded to the length of the longest data shard.
func (rs *ReedSolomon) Encode(data [][]byte) ([][]byte, error) {
	if len(data) != rs.k {
		return nil, fmt.Errorf("expected %d data shards, got %d", rs.k, len(data))
	}

	maxLen := 0
	for _, shard := range data {
		if len(shard) > maxLen {
			maxLen = len(shard)
		}
	}

	parities := make([][]byte, rs.m)
	for i := 0; i < rs.m; i++ {
		parities[i] = make([]byte, maxLen)
	}

	// For each parity row in generator (from row k to k+m-1)
	for p := 0; p < rs.m; p++ {
		genRow := rs.k + p
		for d := 0; d < rs.k; d++ {
			coeff := rs.generator.Get(genRow, d)
			if coeff == 0 {
				continue
			}
			dataShard := data[d]
			for byteIdx := 0; byteIdx < len(dataShard); byteIdx++ {
				parities[p][byteIdx] ^= gfMul(coeff, dataShard[byteIdx])
			}
		}
	}

	return parities, nil
}

// Reconstruct recovers missing data shards from any K received shards.
// The receivedShards map keys are 0..(K-1) for data shards, and K..(K+M-1) for parity shards.
// Returns a map of reconstructed data shards (keys 0..K-1).
func (rs *ReedSolomon) Reconstruct(receivedShards map[int][]byte, maxPktLen int) (map[int][]byte, error) {
	if len(receivedShards) < rs.k {
		return nil, fmt.Errorf("%w: have %d, need %d", ErrNotEnoughShards, len(receivedShards), rs.k)
	}

	// Check if all data shards are already present
	allDataPresent := true
	for i := 0; i < rs.k; i++ {
		if _, ok := receivedShards[i]; !ok {
			allDataPresent = false
			break
		}
	}
	if allDataPresent {
		result := make(map[int][]byte, rs.k)
		for i := 0; i < rs.k; i++ {
			result[i] = receivedShards[i]
		}
		return result, nil
	}

	// Select first K received shards
	selectedIndices := make([]int, 0, rs.k)
	for idx := 0; idx < rs.k+rs.m; idx++ {
		if _, ok := receivedShards[idx]; ok {
			selectedIndices = append(selectedIndices, idx)
			if len(selectedIndices) == rs.k {
				break
			}
		}
	}

	// Extract the K x K submatrix corresponding to the selected received shards
	subMat, err := rs.generator.SubMatrix(selectedIndices)
	if err != nil {
		return nil, fmt.Errorf("failed to extract submatrix: %w", err)
	}

	// Invert the submatrix
	invMat, err := subMat.Invert()
	if err != nil {
		return nil, fmt.Errorf("failed to invert recovery matrix: %w", err)
	}

	// Reconstruct all data shards (0 to K-1)
	reconstructed := make(map[int][]byte, rs.k)

	// Copy already available data shards
	for i := 0; i < rs.k; i++ {
		if shard, ok := receivedShards[i]; ok {
			reconstructed[i] = shard
		}
	}

	// For any missing data shard i:
	for missingIdx := 0; missingIdx < rs.k; missingIdx++ {
		if _, ok := reconstructed[missingIdx]; ok {
			continue // Already present
		}

		recovered := make([]byte, maxPktLen)
		for j, shardIdx := range selectedIndices {
			coeff := invMat.Get(missingIdx, j)
			if coeff == 0 {
				continue
			}
			shard := receivedShards[shardIdx]
			for byteIdx := 0; byteIdx < len(shard); byteIdx++ {
				recovered[byteIdx] ^= gfMul(coeff, shard[byteIdx])
			}
		}
		reconstructed[missingIdx] = recovered
	}

	return reconstructed, nil
}
