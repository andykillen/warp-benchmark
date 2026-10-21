package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

func id() string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	config := flag.String("config", "config.json", "suite configuration")
	out := flag.String("out", "results", "local results directory")
	dry := flag.Bool("plan", false, "save workload plans without benchmarking")
	retry := flag.Bool("replay", false, "publish unsent results from -out")
	reportOnly := flag.Bool("report", false, "print and save a report for the suite directory supplied with -out")
	flag.Parse()
	if *reportOnly {
		return printReport(*out)
	}
	c, e := loadConfig(*config)
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e = os.MkdirAll(*out, 0700); e != nil {
		return e
	}
	if *retry {
		return replay(ctx, c, *out)
	}
	if !*dry && len(c.WRK) > 0 {
		if _, e = exec.LookPath("wrk"); e != nil {
			return fmt.Errorf("wrk not found in PATH; install it or remove wrk profiles")
		}
	}
	paths := []string{}
	for _, x := range c.Cases {
		if x.Kind == "sitemap" {
			paths, e = discoverPaths(ctx, c)
			if e != nil {
				return e
			}
			break
		}
	}
	suiteID := id()
	dir := filepath.Join(*out, suiteID)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	plans := map[string][]WorkItem{}
	for i, x := range c.Cases {
		p := buildPlan(c, x, paths)
		if e = campaignPlan(p, c, x); e != nil {
			return e
		}
		plans[x.Name] = p
		if e = writeJSON(filepath.Join(dir, fmt.Sprintf("plan-%03d.json", i)), p); e != nil {
			return e
		}
	}
	if len(paths) > 0 {
		if e = os.WriteFile(filepath.Join(dir, "paths.txt"), []byte(strings.Join(paths, "\n")+"\n"), 0600); e != nil {
			return e
		}
	}
	if *dry {
		fmt.Printf("Workload plans: %s\n", dir)
		return nil
	}
	host, _ := os.Hostname()
	var failed atomic.Bool
	makeResult := func(t Target, x Case, kind string) Result {
		return Result{SchemaVersion: 1, ID: id(), SuiteID: suiteID, Suite: c.Name, Target: t, Case: x.Name, Kind: kind, CacheMode: x.CacheMode, StartedAt: time.Now().UTC(), Seed: c.Seed, PlanHash: planHash(plans[x.Name]), Machine: host, Revision: os.Getenv("BENCH_REVISION")}
	}
	runTarget := func(t Target, sequential, load bool) error {
		if sequential {
			for _, x := range c.Cases {
				for pass := 1; pass <= t.Repeats; pass++ {
					if ctx.Err() != nil {
						return fmt.Errorf("suite cancelled; completed results are saved in %s", dir)
					}
					fmt.Printf("%s / %s / repeat %d of %d: %d requests, concurrency %d (live progress every second)\n", t.Name, x.Name, pass, t.Repeats, len(plans[x.Name]), c.Concurrency)
					r := makeResult(t, x, "sequential")
					r.Repeat, r.RepeatCount = pass, t.Repeats
					r.Concurrency = c.Concurrency
					r.Samples, r.Stats = runSequentialProgress(ctx, c, t, x, plans[x.Name], func(attempted, successful, errors int, elapsed, averageMS float64) {
						rps := 0.0
						if elapsed > 0 {
							rps = float64(successful) / elapsed
						}
						fmt.Printf("\r\033[2KDone %d/%d | pass %d/%d | %.0fs | avg %.2f req/s, %.0fms | err %d", attempted, len(plans[x.Name]), pass, t.Repeats, elapsed, rps, averageMS, errors)
					})
					if r.Stats.Errors > 0 || r.Stats.Attempted < len(plans[x.Name]) {
						failed.Store(true)
						r.Failure = "requests failed or run incomplete"
					}
					if err := deliver(ctx, c, dir, r); err != nil {
						failed.Store(true)
						fmt.Printf("Result saved; delivery failed: %v\n", err)
					}
					fmt.Printf("\n  %.3fs, %.2f successful requests/s, ave %.0fms per request, %d errors\n", r.Stats.ElapsedSeconds, r.Stats.RequestsPerSecond, r.Stats.LatencyMS["average"], r.Stats.Errors)
				}
			}
		}
		if load {
			for _, p := range c.WRK {
				if ctx.Err() != nil {
					return fmt.Errorf("suite cancelled")
				}
				var x Case
				for _, v := range c.Cases {
					if v.Name == p.Case {
						x = v
					}
				}
				fmt.Printf("%s / wrk %s: %dt %dc %ds\n", t.Name, p.Name, p.Threads, p.Connections, p.DurationSeconds)
				r := makeResult(t, x, "wrk")
				r.Case = p.Name
				w, err := runWRK(ctx, c, t, x, p)
				r.WRK = &w
				if err != nil {
					r.Failure = err.Error()
					failed.Store(true)
				}
				if w.Non2xx3xx > 0 {
					r.Failure = "wrk returned non-2xx/3xx responses"
					failed.Store(true)
				}
				for _, n := range w.SocketErrors {
					if n > 0 {
						r.Failure = "wrk socket errors"
						failed.Store(true)
					}
				}
				if err := deliver(ctx, c, dir, r); err != nil {
					failed.Store(true)
					fmt.Printf("Result saved; delivery failed: %v\n", err)
				}
			}
		}
		return nil
	}
	if c.ParallelTargets {
		if err := executeTargets(c.Targets, true, func(t Target) error { return runTarget(t, true, false) }); err != nil {
			return err
		}
		if err := executeTargets(c.Targets, false, func(t Target) error { return runTarget(t, false, true) }); err != nil {
			return err
		}
	} else {
		if err := executeTargets(c.Targets, false, func(t Target) error { return runTarget(t, true, true) }); err != nil {
			return err
		}
	}
	if err := printReport(dir); err != nil {
		return err
	}
	fmt.Printf("Results: %s\n", dir)
	if failed.Load() {
		return fmt.Errorf("suite completed with failures; inspect results and use -replay for unsent uploads")
	}
	return nil
}
