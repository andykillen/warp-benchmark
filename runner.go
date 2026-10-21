package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type Sample struct {
	Index     int               `json:"index"`
	Path      string            `json:"path"`
	Scenario  Scenario          `json:"scenario"`
	Status    int               `json:"status"`
	HeadersMS float64           `json:"response_headers_ms"`
	LatencyMS float64           `json:"latency_ms"`
	Bytes     int64             `json:"bytes"`
	Error     string            `json:"error,omitempty"`
	Headers   map[string]string `json:"response_headers,omitempty"`
}
type Stats struct {
	Attempted         int                `json:"attempted"`
	Completed         int                `json:"completed"`
	Errors            int                `json:"errors"`
	Bytes             int64              `json:"bytes"`
	ElapsedSeconds    float64            `json:"elapsed_seconds"`
	RequestsPerSecond float64            `json:"requests_per_second"`
	LatencyMS         map[string]float64 `json:"latency_ms"`
	StatusCounts      map[int]int        `json:"status_counts"`
}
type Result struct {
	Concurrency   int       `json:"concurrency,omitempty"`
	Repeat        int       `json:"repeat,omitempty"`
	RepeatCount   int       `json:"repeat_count,omitempty"`
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	SuiteID       string    `json:"suite_id"`
	Suite         string    `json:"suite"`
	Target        Target    `json:"target"`
	Case          string    `json:"case"`
	Kind          string    `json:"kind"`
	CacheMode     string    `json:"cache_mode"`
	StartedAt     time.Time `json:"started_at"`
	Seed          int64     `json:"seed"`
	PlanHash      string    `json:"plan_hash"`
	Machine       string    `json:"machine"`
	Revision      string    `json:"revision,omitempty"`
	Stats         Stats     `json:"stats"`
	Samples       []Sample  `json:"samples,omitempty"`
	WRK           *WRKStats `json:"wrk,omitempty"`
	Failure       string    `json:"failure,omitempty"`
}

func planHash(plan []WorkItem) string {
	b, _ := json.Marshal(plan)
	v := sha256.Sum256(b)
	return hex.EncodeToString(v[:])
}
func expectedStatus(x Case, s int) bool {
	if len(x.ExpectedStatuses) == 0 {
		return s >= 200 && s < 300
	}
	for _, v := range x.ExpectedStatuses {
		if v == s {
			return true
		}
	}
	return false
}
func runSequential(ctx context.Context, c Config, t Target, x Case, plan []WorkItem) ([]Sample, Stats) {
	return runSequentialProgress(ctx, c, t, x, plan, nil)
}

// Progress refreshes once per second, including while a request is pending.
func runSequentialProgress(ctx context.Context, c Config, t Target, x Case, plan []WorkItem, progress func(attempted, successful, errors int, elapsed, averageMS float64)) ([]Sample, Stats) {
	client := newClient(c.TimeoutSeconds)
	defer client.CloseIdleConnections()
	debug := os.Getenv("BENCH_DEBUG") == "1"
	traceID := id()
	trace := func(index int, event, detail string) {
		if debug {
			fmt.Fprintf(os.Stderr, "%s stream=%s target=%q case=%q request=%d %s %s\n", time.Now().Format(time.RFC3339Nano), traceID, t.Name, x.Name, index+1, event, detail)
		}
	}
	traceError := func(index int, stage string, err error) {
		if err == nil {
			return
		}
		if wrapped, ok := err.(*url.Error); ok {
			err = wrapped.Err
		}
		trace(index, stage, fmt.Sprintf("%T: %v", err, err))
	}

	samples := make([]Sample, 0, len(plan))
	started := time.Now()
	successful, errors := 0, 0
	latencyTotalMS := 0.0
	var mu sync.Mutex
	report := func() {
		mu.Lock()
		defer mu.Unlock()
		if progress != nil {
			averageMS := 0.0
			if successful > 0 {
				averageMS = latencyTotalMS / float64(successful)
			}
			progress(len(samples), successful, errors, time.Since(started).Seconds(), averageMS)
		}
	}
	if progress != nil {
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					report()
				case <-stop:
					return
				}
			}
		}()
		defer func() { close(stop); <-done; report() }()
	}
	record := func(sample Sample) {
		mu.Lock()
		defer mu.Unlock()
		samples = append(samples, sample)
		trace(sample.Index, "END", fmt.Sprintf("status=%d headers_ms=%.2f total_ms=%.2f bytes=%d error=%q", sample.Status, sample.HeadersMS, sample.LatencyMS, sample.Bytes, sample.Error))
		if sample.Error == "" {
			successful++
			latencyTotalMS += sample.LatencyMS
		} else {
			errors++
		}
	}

	process := func(i int) {
		item := plan[i]
		if ctx.Err() != nil {
			return
		}
		trace(i, "START", fmt.Sprintf("device=%s country=%s session=%s currency=%q", item.Scenario.Device, item.Scenario.Country, item.Scenario.Session, item.Scenario.Currency))
		sample := Sample{Index: i, Path: item.Path, Scenario: item.Scenario}
		endpoint, _ := requestURL(t.BaseURL, item.Path)
		req, err := http.NewRequestWithContext(ctx, x.Method, endpoint, strings.NewReader(x.Body))
		if err != nil {
			sample.Error = "invalid request"
		} else {
			req.Header, err = scenarioHeaders(c, x, item.Scenario)
			if err != nil {
				sample.Error = "invalid request headers"
			}
		}
		if sample.Error != "" {
			record(sample)
			return
		}
		begin := time.Now()
		resp, err := client.Do(req)
		// This measures time to response headers; body-complete latency follows below.
		sample.HeadersMS = float64(time.Since(begin)) / float64(time.Millisecond)
		if err != nil {
			sample.Error = "transport error"
			traceError(i, "TRANSPORT_ERROR", err)
			if ctx.Err() != nil {
				sample.Error = "cancelled"
			}
		} else {
			trace(i, "HEADERS", fmt.Sprintf("status=%d after_ms=%.2f", resp.StatusCode, sample.HeadersMS))
			sample.Status = resp.StatusCode
			sample.Headers = map[string]string{}
			for _, k := range []string{"Cache-Control", "Warp-Cache-Control-Header", "X-Warp-Cache-Control", "X-Warp-Template", "Content-Encoding", "Age", "X-Cache"} {
				if v := resp.Header.Get(k); v != "" {
					sample.Headers[k] = v
				}
			}
			sample.Bytes, err = io.Copy(io.Discard, resp.Body)
			closeErr := resp.Body.Close()
			traceError(i, "BODY_ERROR", err)
			traceError(i, "CLOSE_ERROR", closeErr)
			if err != nil || closeErr != nil {
				sample.Error = "response body read failed"
			} else if !expectedStatus(x, resp.StatusCode) {
				sample.Error = fmt.Sprintf("unexpected HTTP %d", resp.StatusCode)
			}
		}
		sample.LatencyMS = float64(time.Since(begin)) / float64(time.Millisecond)
		record(sample)
	}
	concurrency := c.Concurrency
	if concurrency == 0 {
		concurrency = 1
	}
	if concurrency < 1 || concurrency > 20 {
		panic("invalid request concurrency")
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				process(index)
			}
		}()
	}
enqueue:
	for index := range plan {
		select {
		case <-ctx.Done():
			break enqueue
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	mu.Lock()
	sort.Slice(samples, func(i, j int) bool { return samples[i].Index < samples[j].Index })
	mu.Unlock()

	return samples, summarise(samples, time.Since(started).Seconds())
}
func summarise(samples []Sample, elapsed float64) Stats {
	s := Stats{Attempted: len(samples), ElapsedSeconds: elapsed, StatusCounts: map[int]int{}, LatencyMS: map[string]float64{}}
	lat := []float64{}
	for _, v := range samples {
		s.Bytes += v.Bytes
		if v.Status != 0 {
			s.StatusCounts[v.Status]++
		}
		if v.Error != "" {
			s.Errors++
		} else {
			s.Completed++
			lat = append(lat, v.LatencyMS)
		}
	}
	if elapsed > 0 {
		s.RequestsPerSecond = float64(s.Completed) / elapsed
	}
	sort.Float64s(lat)
	if len(lat) > 0 {
		sum := 0.0
		for _, v := range lat {
			sum += v
		}
		s.LatencyMS["average"] = sum / float64(len(lat))
		s.LatencyMS["min"] = lat[0]
		s.LatencyMS["max"] = lat[len(lat)-1]
		for k, p := range map[string]float64{"p50": .50, "p75": .75, "p90": .90, "p95": .95, "p99": .99} {
			s.LatencyMS[k] = lat[int(math.Ceil(p*float64(len(lat))))-1]
		}
	}
	return s
}
