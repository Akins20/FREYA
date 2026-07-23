package bench

import "sort"

// The suite registry.
//
// Each category lives in its own file and registers its benchmarks in an init.
// Adding a category is adding a file — nothing here changes — which is what lets
// the suite grow to hundreds of tasks without a central list to keep in sync.

var registry []Benchmark

// Register adds benchmarks to the suite. Called from category files' init.
func Register(bs ...Benchmark) { registry = append(registry, bs...) }

// All returns every registered benchmark, sorted by category then name for a
// stable run order.
func AllBenchmarks() []Benchmark {
	out := append([]Benchmark(nil), registry...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Categories lists the distinct categories present.
func Categories() []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range registry {
		if !seen[b.Category] {
			seen[b.Category] = true
			out = append(out, b.Category)
		}
	}
	sort.Strings(out)
	return out
}

// InCategory returns the benchmarks for one category.
func InCategory(cat string) []Benchmark {
	var out []Benchmark
	for _, b := range AllBenchmarks() {
		if b.Category == cat {
			out = append(out, b)
		}
	}
	return out
}
