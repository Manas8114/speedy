package fec

import (
	"fmt"
)

// BuildCauchyMatrix constructs a systematic generator matrix of size (K + M) x K.
// The top K rows form the Identity matrix I_K (systematic data packets).
// The bottom M rows form a Cauchy matrix where entry P[i,j] = 1 / (x_i ^ y_j),
// which guarantees that every square submatrix is invertible.
func BuildCauchyMatrix(k, m int) (*Matrix, error) {
	if k <= 0 || m <= 0 {
		return nil, fmt.Errorf("invalid matrix dimensions: k=%d, m=%d must be positive", k, m)
	}
	if k+m > 256 {
		return nil, fmt.Errorf("k+m=%d exceeds GF(2^8) field size 256", k+m)
	}

	totalRows := k + m
	gen := NewMatrix(totalRows, k)

	// Systematic Identity part (first k rows)
	for i := 0; i < k; i++ {
		gen.Set(i, i, 1)
	}

	// Cauchy parity part (rows k to k+m-1)
	// Choose disjoint subsets X and Y:
	// X = { 0, 1, ..., m-1 }
	// Y = { m, m+1, ..., m+k-1 }
	for i := 0; i < m; i++ {
		xi := byte(i)
		for j := 0; j < k; j++ {
			yj := byte(m + j)
			diff := xi ^ yj
			gen.Set(k+i, j, gfInv(diff))
		}
	}

	return gen, nil
}

// SubMatrix extracts a subset of rows from the matrix specified by rowIndices
func (m *Matrix) SubMatrix(rowIndices []int) (*Matrix, error) {
	sub := NewMatrix(len(rowIndices), m.Cols)
	for newRow, origRow := range rowIndices {
		if origRow < 0 || origRow >= m.Rows {
			return nil, fmt.Errorf("row index %d out of bounds (0..%d)", origRow, m.Rows-1)
		}
		for c := 0; c < m.Cols; c++ {
			sub.Set(newRow, c, m.Get(origRow, c))
		}
	}
	return sub, nil
}
