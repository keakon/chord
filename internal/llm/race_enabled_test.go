//go:build race

package llm

// raceBuildEnabled reports whether the test binary runs under the race
// detector. Sonic swaps in its non-JIT compatibility decoder there, which
// inflates allocation-byte measurements by orders of magnitude.
const raceBuildEnabled = true
