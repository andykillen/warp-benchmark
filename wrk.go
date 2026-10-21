package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type WRKStats struct {
	Threads                 int                `json:"threads"`
	Connections             int                `json:"connections"`
	DurationSeconds         int                `json:"duration_seconds"`
	TotalRequests           int64              `json:"total_requests"`
	ElapsedSeconds          float64            `json:"elapsed_seconds"`
	RequestsPerSecond       float64            `json:"requests_per_second"`
	Bytes                   float64            `json:"bytes"`
	BytesPerSecond          float64            `json:"bytes_per_second"`
	LatencyMS               map[string]float64 `json:"latency_ms"`
	ThreadRequestsPerSecond map[string]float64 `json:"thread_requests_per_second"`
	SocketErrors            map[string]int64   `json:"socket_errors"`
	Non2xx3xx               int64              `json:"non_2xx_3xx"`
}

func scalar(v string, kind string) (float64, error) {
	r := regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([A-Za-z]*)$`)
	m := r.FindStringSubmatch(v)
	if m == nil {
		return 0, fmt.Errorf("invalid wrk value")
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	var scales map[string]float64
	switch kind {
	case "time":
		scales = map[string]float64{"us": .001, "ms": 1, "s": 1000, "m": 60000}
	case "bytes":
		scales = map[string]float64{"B": 1, "KB": 1024, "MB": 1048576, "GB": 1073741824, "TB": 1099511627776}
	default:
		scales = map[string]float64{"": 1, "k": 1e3, "M": 1e6, "G": 1e9}
	}
	k, ok := scales[m[2]]
	if !ok {
		return 0, fmt.Errorf("unknown wrk unit")
	}
	return n * k, nil
}
func parseWRK(raw string) (WRKStats, error) {
	w := WRKStats{LatencyMS: map[string]float64{}, ThreadRequestsPerSecond: map[string]float64{}, SocketErrors: map[string]int64{}}
	gotSummary, gotRPS, gotLatency, gotTransfer := false, false, false, false
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		var err error
		switch {
		case f[0] == "Latency" && len(f) == 5:
			gotLatency = true
			for i, k := range []string{"average", "stdev", "max"} {
				w.LatencyMS[k], err = scalar(f[i+1], "time")
				if err != nil {
					return w, err
				}
			}
			w.LatencyMS["within_stdev_percent"], err = strconv.ParseFloat(strings.TrimSuffix(f[4], "%"), 64)
		case f[0] == "Req/Sec" && len(f) == 5:
			for i, k := range []string{"average", "stdev", "max"} {
				w.ThreadRequestsPerSecond[k], err = scalar(f[i+1], "count")
				if err != nil {
					return w, err
				}
			}
			w.ThreadRequestsPerSecond["within_stdev_percent"], err = strconv.ParseFloat(strings.TrimSuffix(f[4], "%"), 64)
		case strings.HasSuffix(f[0], "%") && len(f) == 2:
			w.LatencyMS["p"+strings.TrimSuffix(f[0], "%")], err = scalar(f[1], "time")
		case len(f) >= 6 && f[1] == "requests" && f[2] == "in":
			gotSummary = true
			w.TotalRequests, err = strconv.ParseInt(f[0], 10, 64)
			if err != nil {
				return w, err
			}
			v, e := scalar(strings.TrimSuffix(f[3], ","), "time")
			if e != nil {
				return w, e
			}
			w.ElapsedSeconds = v / 1000
			w.Bytes, err = scalar(f[4], "bytes")
		case f[0] == "Requests/sec:" && len(f) == 2:
			gotRPS = true
			w.RequestsPerSecond, err = strconv.ParseFloat(f[1], 64)
		case f[0] == "Transfer/sec:" && len(f) == 2:
			gotTransfer = true
			w.BytesPerSecond, err = scalar(f[1], "bytes")
		case strings.HasPrefix(strings.TrimSpace(line), "Socket errors:"):
			m := regexp.MustCompile(`(connect|read|write|timeout)\s+(\d+)`).FindAllStringSubmatch(line, -1)
			for _, p := range m {
				w.SocketErrors[p[1]], err = strconv.ParseInt(p[2], 10, 64)
			}
		case strings.Contains(line, "Non-2xx or 3xx responses:"):
			w.Non2xx3xx, err = strconv.ParseInt(f[len(f)-1], 10, 64)
		}
		if err != nil {
			return w, err
		}
	}
	if !gotSummary || !gotRPS || !gotLatency || !gotTransfer {
		return w, fmt.Errorf("incomplete or unsupported wrk output")
	}
	return w, nil
}
func luaQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, v := range []byte(s) {
		if v == '"' || v == '\\' {
			b.WriteByte('\\')
			b.WriteByte(v)
		} else if v < 32 || v > 126 {
			fmt.Fprintf(&b, "\\%03d", v)
		} else {
			b.WriteByte(v)
		}
	}
	b.WriteByte('"')
	return b.String()
}
func runWRK(ctx context.Context, c Config, t Target, x Case, p WRKProfile) (WRKStats, error) {
	s := Scenario{x.Device, x.Country, x.Session, *x.Currency}
	h, e := scenarioHeaders(c, x, s)
	if e != nil {
		return WRKStats{}, e
	}
	// A private script avoids putting cookie credentials in process arguments.
	file, e := os.CreateTemp("", "warp-bench-*.lua")
	if e != nil {
		return WRKStats{}, e
	}
	defer os.Remove(file.Name())
	script := "wrk.method = " + luaQuote(x.Method) + "\nwrk.body = " + luaQuote(x.Body) + "\n"
	for k, v := range h {
		script += "wrk.headers[" + luaQuote(k) + "] = " + luaQuote(v[0]) + "\n"
	}
	plan := buildPlan(c, x, nil)
	if e = campaignPlan(plan, c, x); e != nil {
		file.Close()
		return WRKStats{}, e
	}
	script += "local paths = {\n"
	for _, v := range plan {
		script += luaQuote(v.Path) + ",\n"
	}
	script += "}\nlocal i = 0\nrequest = function()\n i = (i % #paths) + 1\n return wrk.format(wrk.method, paths[i], wrk.headers, wrk.body)\nend\n"
	if _, e = file.WriteString(script); e != nil {
		file.Close()
		return WRKStats{}, e
	}
	if e = file.Close(); e != nil {
		return WRKStats{}, e
	}
	testCtx, cancel := context.WithTimeout(ctx, time.Duration(p.DurationSeconds+c.TimeoutSeconds+10)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(testCtx, "wrk", fmt.Sprintf("-t%d", p.Threads), fmt.Sprintf("-c%d", p.Connections), fmt.Sprintf("-d%ds", p.DurationSeconds), fmt.Sprintf("--timeout=%ds", c.TimeoutSeconds), "--latency", "-s", file.Name(), t.BaseURL)
	out, runErr := cmd.CombinedOutput()
	w, parseErr := parseWRK(string(out))
	w.Threads = p.Threads
	w.Connections = p.Connections
	w.DurationSeconds = p.DurationSeconds
	// Raw subprocess output is deliberately not persisted: Lua errors may expose
	// request secrets. Parsed metrics preserve wrk's precision and error counts.
	if runErr != nil {
		return w, fmt.Errorf("wrk failed (exit or cancellation)")
	}
	if parseErr != nil {
		return w, parseErr
	}
	return w, nil
}
