package fec

import (
	"errors"
	"fmt"
)

var (
	ErrSingularMatrix    = errors.New("matrix is singular and cannot be inverted")
	ErrDimensionMismatch = errors.New("matrix dimensions do not match operation requirements")
)

// gf256 represents operations over Galois Field GF(2^8) with primitive polynomial 0x11d (x^8 + x^4 + x^3 + x^2 + 1)
type gf256 struct {
	exp [512]byte
	log [256]byte
}

var gf = newGF256()

func newGF256() *gf256 {
	g := &gf256{}
	// Primitive polynomial 0x11d = 285
	poly := 0x11d
	val := 1
	for i := 0; i < 255; i++ {
		g.exp[i] = byte(val)
		g.exp[i+255] = byte(val)
		g.log[val] = byte(i)
		val <<= 1
		if val >= 256 {
			val ^= poly
		}
	}
	g.log[0] = 0 // Undefined mathematically, but set for safety
	return g
}

// Add returns a + b in GF(2^8) (bitwise XOR)
func gfAdd(a, b byte) byte {
	return a ^ b
}

// Sub is identical to Add in characteristic 2
func gfSub(a, b byte) byte {
	return a ^ b
}

// Mul returns a * b in GF(2^8)
func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gf.exp[int(gf.log[a])+int(gf.log[b])]
}

// Div returns a / b in GF(2^8)
func gfDiv(a, b byte) byte {
	if b == 0 {
		panic("GF(2^8) division by zero")
	}
	if a == 0 {
		return 0
	}
	logA := int(gf.log[a])
	logB := int(gf.log[b])
	diff := logA - logB
	if diff < 0 {
		diff += 255
	}
	return gf.exp[diff]
}

// Inv returns the multiplicative inverse 1 / a in GF(2^8)
func gfInv(a byte) byte {
	if a == 0 {
		panic("GF(2^8) inverse of zero")
	}
	return gf.exp[255-int(gf.log[a])]
}

// Matrix represents a 2D matrix over GF(2^8)
type Matrix struct {
	Rows int
	Cols int
	Data []byte // Row-major: data[r*Cols + c]
}

// NewMatrix allocates a zero-initialized matrix
func NewMatrix(rows, cols int) *Matrix {
	return &Matrix{
		Rows: rows,
		Cols: cols,
		Data: make([]byte, rows*cols),
	}
}

// NewIdentityMatrix creates an n x n identity matrix in GF(2^8)
func NewIdentityMatrix(n int) *Matrix {
	m := NewMatrix(n, n)
	for i := 0; i < n; i++ {
		m.Set(i, i, 1)
	}
	return m
}

func (m *Matrix) Get(r, c int) byte {
	return m.Data[r*m.Cols+c]
}

func (m *Matrix) Set(r, c int, val byte) {
	m.Data[r*m.Cols+c] = val
}

// Multiply multiplies m (r x k) by other (k x c), returning (r x c)
func (m *Matrix) Multiply(other *Matrix) (*Matrix, error) {
	if m.Cols != other.Rows {
		return nil, fmt.Errorf("%w: cannot multiply %dx%d by %dx%d", ErrDimensionMismatch, m.Rows, m.Cols, other.Rows, other.Cols)
	}
	res := NewMatrix(m.Rows, other.Cols)
	for r := 0; r < m.Rows; r++ {
		for c := 0; c < other.Cols; c++ {
			var acc byte
			for k := 0; k < m.Cols; k++ {
				acc ^= gfMul(m.Get(r, k), other.Get(k, c))
			}
			res.Set(r, c, acc)
		}
	}
	return res, nil
}

// Invert computes the inverse of a square matrix using Gaussian-Jordan elimination
func (m *Matrix) Invert() (*Matrix, error) {
	if m.Rows != m.Cols {
		return nil, fmt.Errorf("%w: matrix must be square, got %dx%d", ErrDimensionMismatch, m.Rows, m.Cols)
	}
	n := m.Rows

	// Augment matrix with Identity: [m | I]
	work := NewMatrix(n, 2*n)
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			work.Set(r, c, m.Get(r, c))
		}
		work.Set(r, n+r, 1)
	}

	// Forward elimination and back substitution
	for col := 0; col < n; col++ {
		// Pivot: find non-zero entry in column
		pivotRow := -1
		for row := col; row < n; row++ {
			if work.Get(row, col) != 0 {
				pivotRow = row
				break
			}
		}
		if pivotRow == -1 {
			return nil, ErrSingularMatrix
		}

		// Swap pivot row with current row if necessary
		if pivotRow != col {
			for c := 0; c < 2*n; c++ {
				tmp := work.Get(col, c)
				work.Set(col, c, work.Get(pivotRow, c))
				work.Set(pivotRow, c, tmp)
			}
		}

		// Scale pivot row so leading coefficient is 1
		pivotVal := work.Get(col, col)
		if pivotVal != 1 {
			pivotInv := gfInv(pivotVal)
			for c := col; c < 2*n; c++ {
				work.Set(col, c, gfMul(work.Get(col, c), pivotInv))
			}
		}

		// Eliminate all other rows in this column
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := work.Get(row, col)
			if factor != 0 {
				for c := col; c < 2*n; c++ {
					work.Set(row, c, work.Get(row, c)^gfMul(factor, work.Get(col, c)))
				}
			}
		}
	}

	// Extract right-hand side inverse matrix
	inv := NewMatrix(n, n)
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			inv.Set(r, c, work.Get(r, n+c))
		}
	}
	return inv, nil
}
