// Command bench runs Freya against the agentic benchmark suite and scores her.
//
// It drives the real freya binary in throwaway workspaces and checks the
// artifacts she leaves behind, so a pass means work was actually done — not that
// a plausible sentence was produced. It needs a live model: point FREYA_PROVIDER
// and the matching API key at a real backend, or the agent has nothing to think
// with.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/akins/jarvis/internal/bench"
)

func main() {
	var (
		binary   = flag.String("bin", "", "path to the freya binary (default: ./bin/freya then $PATH)")
		category = flag.String("category", "", "run only this category")
		only     = flag.String("only", "", "run only benchmarks whose name contains this")
		list     = flag.Bool("list", false, "list benchmarks and exit")
		parallel = flag.Int("parallel", 2, "how many benchmarks to run at once")
		keep     = flag.Bool("keep", false, "keep workspaces on disk for inspection")
		verbose  = flag.Bool("v", false, "stream per-run progress")
	)
	flag.Parse()

	all := bench.AllBenchmarks()
	if *category != "" {
		all = bench.InCategory(*category)
	}
	if *only != "" {
		var f []bench.Benchmark
		for _, b := range all {
			if strings.Contains(b.Name, *only) {
				f = append(f, b)
			}
		}
		all = f
	}

	if *list {
		fmt.Printf("%d benchmarks across %d categories:\n", len(bench.AllBenchmarks()), len(bench.Categories()))
		for _, c := range bench.Categories() {
			bs := bench.InCategory(c)
			fmt.Printf("\n%s (%d)\n", c, len(bs))
			for _, b := range bs {
				fmt.Printf("  %-30s %s\n", b.Name, strings.Repeat("★", b.Difficulty))
			}
		}
		return
	}

	if len(all) == 0 {
		fmt.Fprintln(os.Stderr, "no benchmarks matched")
		os.Exit(1)
	}

	bin, err := findBinary(*binary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}

	runner := &bench.Runner{Binary: bin, KeepWorkdirs: *keep, Verbose: *verbose}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("running %d benchmarks against %s (parallel %d)\n\n", len(all), bin, *parallel)
	start := time.Now()

	// A bounded worker pool: benchmarks are independent, and running a few at
	// once cuts a suite that is mostly waiting on the model from an hour to
	// minutes, without hammering the API.
	var (
		card bench.Scorecard
		mu   sync.Mutex
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, maxi(1, *parallel))
	for _, b := range all {
		wg.Add(1)
		go func(b bench.Benchmark) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			r := runner.Run(ctx, b)
			mu.Lock()
			card.Add(r)
			mark := "FAIL"
			if r.Pass {
				mark = "pass"
			}
			fmt.Printf("  [%s] %-28s %s\n", mark, b.Name, r.Reason)
			mu.Unlock()
		}(b)
	}
	wg.Wait()

	fmt.Printf("\n%s\n", card.Report())
	fmt.Printf("(suite took %s)\n", time.Since(start).Round(time.Second))

	// Exit non-zero if anything failed, so this can gate a release.
	for _, r := range card.Results {
		if !r.Pass {
			os.Exit(1)
		}
	}
}

// findBinary locates the freya executable to test.
func findBinary(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("no binary at %s", explicit)
		}
		// Must be absolute: each run changes the working directory to a throwaway
		// workspace, and a relative binary path would resolve against that empty
		// dir and fail to launch — silently, as 0 tools in 0 seconds.
		return absPath(explicit)
	}
	for _, cand := range []string{"./bin/freya", "bin/freya"} {
		if _, err := os.Stat(cand); err == nil {
			abs, _ := absPath(cand)
			return abs, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := home + "/.local/bin/freya"
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("freya binary not found; build it (make build) or pass -bin")
}

func absPath(p string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return p, err
	}
	if strings.HasPrefix(p, "/") {
		return p, nil
	}
	return wd + "/" + strings.TrimPrefix(p, "./"), nil
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}
