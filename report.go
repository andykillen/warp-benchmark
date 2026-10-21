package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
)

// printReport uses saved results, so it also works on an earlier run.
func printReport(dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "result-*.json"))
	if err != nil {
		return err
	}
	var results []Result
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var result Result
		if err := json.Unmarshal(data, &result); err != nil {
			return fmt.Errorf("read report result %s: %w", path, err)
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.Target.Name != b.Target.Name {
			return a.Target.Name < b.Target.Name
		}
		if a.Case != b.Case {
			return a.Case < b.Case
		}
		if a.Repeat != b.Repeat {
			return a.Repeat < b.Repeat
		}
		return a.StartedAt.Before(b.StartedAt)
	})
	var out bytes.Buffer
	fmt.Fprintln(&out, "\nEnd-of-run report")
	fmt.Fprintln(&out, "Latency averages and percentiles include successful requests only.")
	fmt.Fprintln(&out, "Requests/sec uses the whole pass, including time spent waiting for failures.")
	if len(results) == 0 {
		fmt.Fprintln(&out, "No saved results found.")
	}
	current := ""
	baselines := map[string]Result{}
	failures := 0
	var failureLines []string
	for _, r := range results {
		key := r.Target.Name + "\x00" + r.Case + "\x00" + r.Kind
		if key != current {
			fmt.Fprintf(&out, "\n%s / %s\n", r.Target.Name, r.Case)
			current = key
		}
		if r.WRK != nil {
			w := r.WRK
			fmt.Fprintf(&out, "wrk: %.2f requests/sec, avg %.2fms, p50 %.2fms, p99 %.2fms; HTTP errors %d\n", w.RequestsPerSecond, w.LatencyMS["average"], w.LatencyMS["p50"], w.LatencyMS["p99"], w.Non2xx3xx)
			socketKeys := []string{}
			for name := range w.SocketErrors {
				socketKeys = append(socketKeys, name)
			}
			sort.Strings(socketKeys)
			for _, name := range socketKeys {
				if w.SocketErrors[name] > 0 {
					fmt.Fprintf(&out, "  Socket %s: %d\n", name, w.SocketErrors[name])
				}
			}
			if r.Failure != "" {
				fmt.Fprintf(&out, "  Failure: %s\n", r.Failure)
			}
			continue
		}
		fmt.Fprintf(&out, "Pass %d/%d, concurrency %d: %.3fs, %.2f successful requests/sec\n", r.Repeat, r.RepeatCount, r.Concurrency, r.Stats.ElapsedSeconds, r.Stats.RequestsPerSecond)
		if r.Repeat <= 1 {
			baselines[key] = r
		}
		if first, ok := baselines[key]; ok && r.Repeat > 1 && first.PlanHash == r.PlanHash && first.Stats.ElapsedSeconds > 0 && r.Stats.ElapsedSeconds > 0 {
			fmt.Fprintf(&out, "  Completion speed vs pass 1: %.2fx (includes errors; compare failure counts)\n", first.Stats.ElapsedSeconds/r.Stats.ElapsedSeconds)
		}
		groups := map[string][]Sample{}
		for _, sample := range r.Samples {
			session := sample.Scenario.Session
			if session == "" {
				session = "unspecified"
			}
			groups[session] = append(groups[session], sample)
			if sample.Error != "" {
				failures++
				if len(failureLines) < 20 {
					failureLines = append(failureLines, fmt.Sprintf("%s / %s / pass %d / request %d: %s; %s, %s, %s, currency=%q; status=%d; %.2fms; %s", r.Target.Name, r.Case, r.Repeat, sample.Index+1, sample.Path, sample.Scenario.Session, sample.Scenario.Device, sample.Scenario.Country, sample.Scenario.Currency, sample.Status, sample.LatencyMS, sample.Error))
				}
			}
		}
		names := []string{}
		for name := range groups {
			names = append(names, name)
		}
		sort.Strings(names)
		table := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "Session\tRequests\tOK\tErrors\tAvg ms\tp50 ms\tp95 ms")
		for _, name := range names {
			stats := summarise(groups[name], 0)
			if stats.Completed == 0 {
				fmt.Fprintf(table, "%s\t%d\t%d\t%d\tn/a\tn/a\tn/a\n", name, stats.Attempted, stats.Completed, stats.Errors)
			} else {
				fmt.Fprintf(table, "%s\t%d\t%d\t%d\t%.2f\t%.2f\t%.2f\n", name, stats.Attempted, stats.Completed, stats.Errors, stats.LatencyMS["average"], stats.LatencyMS["p50"], stats.LatencyMS["p95"])
			}
		}
		table.Flush()
		if len(r.Samples) == 0 {
			fmt.Fprintln(&out, "No per-request samples available.")
		}
	}
	if failures > 0 {
		fmt.Fprintf(&out, "\nFailed requests: %d across all passes (showing up to 20)\n", failures)
		for _, line := range failureLines {
			fmt.Fprintln(&out, line)
		}
		if failures > len(failureLines) {
			fmt.Fprintln(&out, "Remaining failures are in the result JSON files.")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), out.Bytes(), 0600); err != nil {
		return err
	}
	fmt.Print(out.String())
	return nil
}
