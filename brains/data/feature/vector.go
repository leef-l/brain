package feature

import (
	"encoding/binary"
	"math"
)

// VectorDim is the fixed dimensionality of the feature vector.
const VectorDim = 192

// Serialize encodes a float64 slice to bytes (little-endian float64).
func Serialize(vec []float64) []byte {
	buf := make([]byte, len(vec)*8)
	for i, v := range vec {
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(v))
	}
	return buf
}

// Deserialize restores a float64 slice from bytes.
func Deserialize(data []byte) []float64 {
	n := len(data) / 8
	vec := make([]float64, n)
	for i := range vec {
		vec[i] = math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:]))
	}
	return vec
}

// ToArray converts a slice to a fixed-size array (for Ring Buffer usage).
func ToArray(vec []float64) [VectorDim]float64 {
	var arr [VectorDim]float64
	copy(arr[:], vec)
	return arr
}

// Normalize applies min-max normalization to [0,1].
// Returns a new slice; the original is not modified.
// If all values are equal, returns a slice of zeros.
func Normalize(vec []float64) []float64 {
	if len(vec) == 0 {
		return nil
	}
	minVal := vec[0]
	maxVal := vec[0]
	for _, v := range vec[1:] {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}
	result := make([]float64, len(vec))
	rng := maxVal - minVal
	if rng == 0 {
		return result // all zeros
	}
	for i, v := range vec {
		result[i] = (v - minVal) / rng
	}
	return result
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns 0 if either vector has zero magnitude.
func CosineSimilarity(a, b []float64) float64 {
	n := len(a)
	if n == 0 || n != len(b) {
		return 0
	}
	var dot, magA, magB float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}
	denom := math.Sqrt(magA) * math.Sqrt(magB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
