package inventory

import "testing"

// TestEffectiveRatio pins the rule a sampled count is extrapolated by: the
// ratio comes out of the data, and an answer it cannot come out of yields 0
// rather than a guess.
func TestEffectiveRatio(t *testing.T) {
	for _, tt := range []struct {
		name                string
		population, matched int64
		want                int64
	}{
		{"as requested", 5_000_000, 50, 100000},
		{"rounded down by the engine", 5000, 50, 100},
		{"not sampled", 50, 50, 1},
		{"nothing matched", 0, 0, 0},
		{"population below the sample", 40, 50, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveRatio(tt.population, tt.matched); got != tt.want {
				t.Errorf("effectiveRatio(%d, %d) = %d, want %d", tt.population, tt.matched, got, tt.want)
			}
		})
	}
}

// TestSampledMissBound: 1-in-R sampling misses a population of 3R with
// probability e^-3, so that is the bound a sampled miss states.
func TestSampledMissBound(t *testing.T) {
	for ratio, want := range map[int64]int64{1000: 3000, 100000: 300000} {
		if got := SampledMissBound(ratio); got != want {
			t.Errorf("SampledMissBound(%d) = %d, want %d", ratio, got, want)
		}
	}
}
