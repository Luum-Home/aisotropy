package semantic

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// defaultEmbedDim is the HashEmbedder's default vector width.
const defaultEmbedDim = 128

// HashEmbedder is a deterministic, dependency-free embedder for local dev and
// tests: it hashes each token into one of Dim buckets (the hashing trick /
// feature hashing) and L2-normalizes the result. It captures lexical overlap —
// enough to exercise the vector path deterministically without a model or
// credentials — but it is NOT semantic: synonyms and paraphrase will not match.
// A real embedding model (a local ONNX/gguf model or a provider embeddings API)
// is a drop-in behind the Embedder interface; this exists so the semantic tier
// runs and is tested with zero external dependencies.
type HashEmbedder struct {
	Dim int
}

// NewHashEmbedder returns a HashEmbedder of the given dimension (<=0 uses the
// default).
func NewHashEmbedder(dim int) HashEmbedder {
	if dim <= 0 {
		dim = defaultEmbedDim
	}
	return HashEmbedder{Dim: dim}
}

// Dimension reports the vector width.
func (h HashEmbedder) Dimension() int {
	if h.Dim <= 0 {
		return defaultEmbedDim
	}
	return h.Dim
}

// Embed maps each input text to a normalized bag-of-hashed-tokens vector.
func (h HashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	dim := h.Dimension()
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, dim)
		for _, tok := range tokenize(t) {
			hh := fnv.New32a()
			_, _ = hh.Write([]byte(tok))
			v[hh.Sum32()%uint32(dim)]++
		}
		l2normalize(v)
		out[i] = v
	}
	return out, nil
}

// tokenize lowercases and splits text into alphanumeric word tokens. Shared by
// the embedder and the lexical reranker so they agree on what a "term" is.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// l2normalize scales v to unit length in place (no-op for a zero vector).
func l2normalize(v []float32) {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
}
