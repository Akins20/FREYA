package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akins/jarvis/internal/config"
	"github.com/akins/jarvis/internal/telemetry"
)

// The usage report.
//
// # Why the numbers are hedged everywhere they appear
//
// Token counts here are exact — the provider returns them. The money is not:
// it is those counts multiplied by a rate table that ships with the binary and
// goes stale the moment a provider changes its pricing. Worse, Claude runs on a
// subscription, so its calls cost nothing per token no matter what any table
// says.
//
// A bare "$2.31" would be believed. Every figure is therefore printed with a
// tilde and followed by a sentence saying what it is not, because an estimate
// that reads as a bill is worse than no estimate at all.

// showStats prints a usage report.
func showStats(cfg *config.Config, arg string, ind *indicator) error {
	path := filepath.Join(cfg.DataDir, "telemetry.jsonl")

	switch {
	case strings.HasPrefix(arg, "rate"):
		fmt.Println(telemetry.Rates())
		return nil

	case strings.HasPrefix(arg, "day"), strings.HasPrefix(arg, "daily"):
		events, err := telemetry.Load(path, time.Time{})
		if err != nil {
			return err
		}
		fmt.Println(telemetry.Daily(events))
		return nil
	}

	// A bare /stats covers everything; /stats 7 covers the last seven days.
	var since time.Time
	window := "all time"
	if days, err := strconv.Atoi(strings.TrimSuffix(arg, "d")); err == nil && days > 0 {
		since = time.Now().AddDate(0, 0, -days)
		window = fmt.Sprintf("last %d days", days)
	}

	events, err := telemetry.Load(path, since)
	if err != nil {
		return err
	}

	fmt.Printf("%s%s%s\n", cDim, window, cReset)
	fmt.Println(telemetry.Summarise(events).Report(15))

	if ind != nil {
		fmt.Printf("\n%s%s%s\n", cDim, ind.describe(), cReset)
	}
	return nil
}
