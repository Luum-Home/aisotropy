package eval

import (
	"math"
	"testing"
)

const eps = 1e-9

func almostEqual(a, b float64) bool { return math.Abs(a-b) < eps }

func TestPrecisionAtK(t *testing.T) {
	tests := []struct {
		name      string
		retrieved []string
		relevant  []string
		k         int
		want      float64
	}{
		{"all relevant", []string{"a", "b"}, []string{"a", "b"}, 2, 1.0},
		{"half relevant", []string{"a", "x"}, []string{"a", "b"}, 2, 0.5},
		{"none relevant", []string{"x", "y"}, []string{"a", "b"}, 2, 0.0},
		{"k larger than returned penalizes gaps", []string{"a"}, []string{"a", "b"}, 4, 0.25},
		{"only top-k counted", []string{"x", "y", "a"}, []string{"a"}, 2, 0.0},
		{"dedup does not inflate", []string{"a", "a"}, []string{"a"}, 2, 0.5},
		{"k=0 is zero", []string{"a"}, []string{"a"}, 0, 0.0},
		{"empty retrieved", nil, []string{"a"}, 3, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrecisionAtK(tt.retrieved, tt.relevant, tt.k); !almostEqual(got, tt.want) {
				t.Fatalf("PrecisionAtK = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecallAtK(t *testing.T) {
	tests := []struct {
		name      string
		retrieved []string
		relevant  []string
		k         int
		want      float64
	}{
		{"all recalled", []string{"a", "b"}, []string{"a", "b"}, 2, 1.0},
		{"half recalled", []string{"a", "x"}, []string{"a", "b"}, 2, 0.5},
		{"cutoff limits recall", []string{"x", "a", "b"}, []string{"a", "b"}, 1, 0.0},
		{"none relevant set", []string{"a"}, nil, 2, 0.0},
		{"dedup relevant labels", []string{"a"}, []string{"a", "a"}, 2, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RecallAtK(tt.retrieved, tt.relevant, tt.k); !almostEqual(got, tt.want) {
				t.Fatalf("RecallAtK = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestF1(t *testing.T) {
	tests := []struct {
		name                    string
		precision, recall, want float64
	}{
		{"both one", 1, 1, 1},
		{"both half", 0.5, 0.5, 0.5},
		{"p high r zero", 1, 0, 0},
		{"asymmetric", 1, 0.5, 2.0 / 3.0},
		{"both zero", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := F1(tt.precision, tt.recall); !almostEqual(got, tt.want) {
				t.Fatalf("F1(%v,%v) = %v, want %v", tt.precision, tt.recall, got, tt.want)
			}
		})
	}
}

func TestNDCGAtK(t *testing.T) {
	// log2(2)=1, log2(3)~1.585, log2(4)=2. With one relevant item, IDCG=1, so
	// nDCG == the discount at the hit's rank; with multiple, it normalizes
	// against the ideal packing.
	log2 := func(x float64) float64 { return math.Log2(x) }
	tests := []struct {
		name      string
		retrieved []string
		relevant  []string
		k         int
		want      float64
	}{
		{"single relevant at rank 1", []string{"a", "x"}, []string{"a"}, 3, 1.0},
		{"single relevant at rank 2", []string{"x", "a"}, []string{"a"}, 3, 1 / log2(3)},
		{"single relevant at rank 3", []string{"x", "y", "a"}, []string{"a"}, 3, 1 / log2(4)},
		{"miss below cutoff", []string{"x", "y", "z", "a"}, []string{"a"}, 3, 0.0},
		{"all relevant ideal order", []string{"a", "b"}, []string{"a", "b"}, 2, 1.0},
		{
			// DCG = 1/log2(2) + 1/log2(4) = 1 + 0.5; IDCG = 1/log2(2) + 1/log2(3).
			"two relevant, one interleaved",
			[]string{"a", "x", "b"}, []string{"a", "b"}, 3,
			(1 + 1/log2(4)) / (1 + 1/log2(3)),
		},
		{"dedup does not double-count", []string{"a", "a"}, []string{"a"}, 2, 1.0},
		{"empty relevant set", []string{"a"}, nil, 3, 0.0},
		{"k=0 is zero", []string{"a"}, []string{"a"}, 0, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NDCGAtK(tt.retrieved, tt.relevant, tt.k); !almostEqual(got, tt.want) {
				t.Fatalf("NDCGAtK = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMRR(t *testing.T) {
	tests := []struct {
		name      string
		retrieved []string
		relevant  []string
		want      float64
	}{
		{"first is relevant", []string{"a", "x"}, []string{"a"}, 1.0},
		{"second is relevant", []string{"x", "a"}, []string{"a"}, 0.5},
		{"third is relevant", []string{"x", "y", "a"}, []string{"a"}, 1.0 / 3.0},
		{"none relevant", []string{"x", "y"}, []string{"a"}, 0.0},
		{"dedup does not advance rank", []string{"x", "x", "a"}, []string{"a"}, 0.5},
		{"empty relevant", []string{"a"}, nil, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MRR(tt.retrieved, tt.relevant); !almostEqual(got, tt.want) {
				t.Fatalf("MRR = %v, want %v", got, tt.want)
			}
		})
	}
}
