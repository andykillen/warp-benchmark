package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Name: "test", Seed: 42, Requests: 1000, TimeoutSeconds: 2, CountryHeader: "X-Country", Countries: []string{"NL", "GB"}, Currencies: []string{"", "EUR", "GBP"}, CurrencyCookie: "currency", SessionCookieEnv: map[string]string{"logged_in": "TEST_LOGIN"}, Publish: PublishConfig{TokenEnv: "TEST_TOKEN", Attempts: 1}}
}
func TestPlanReproducibleBalanced(t *testing.T) {
	c := testConfig()
	x := Case{Name: "products", Kind: "sitemap", Requests: 72}
	p := buildPlan(c, x, []string{"/a/", "/b/"})
	q := buildPlan(c, x, []string{"/a/", "/b/"})
	if !reflect.DeepEqual(p, q) {
		t.Fatal("different plans")
	}
	counts := map[Scenario]int{}
	for _, v := range p {
		counts[v.Scenario]++
	}
	if len(counts) != 36 {
		t.Fatal(len(counts))
	}
	for _, n := range counts {
		if n != 2 {
			t.Fatal("unbalanced", n)
		}
	}
}
func TestCampaigns(t *testing.T) {
	c := testConfig()
	c.Campaign = CampaignConfig{Enabled: true, Probability: 1}
	x := Case{Kind: "endpoint", Path: "/ajax?action=test&gclid=existing", Requests: 2}
	p := buildPlan(c, x, nil)
	q := buildPlan(c, x, nil)
	if e := addCampaigns(p, c); e != nil {
		t.Fatal(e)
	}
	addCampaigns(q, c)
	if !reflect.DeepEqual(p, q) || !strings.Contains(p[0].Path, "gclid=existing") || !strings.Contains(p[0].Path, "utm_source=google") {
		t.Fatal(p)
	}
}
func TestCookieIsolationAndDevices(t *testing.T) {
	t.Setenv("TEST_LOGIN", "wordpress_logged_in_test=abc; currency=old")
	t.Setenv("TEST_OUT", "preference=yes")
	c := testConfig()
	c.SessionCookieEnv["logged_out"] = "TEST_OUT"
	for _, device := range []string{"desktop", "mobile", "tablet"} {
		h, e := scenarioHeaders(c, Case{}, Scenario{device, "NL", "logged_out", ""})
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(h.Get("Cookie"), "wordpress_logged_in") || strings.Contains(h.Get("Cookie"), "currency") {
			t.Fatal(h)
		}
		if h.Get("User-Agent") != devices[device].UA {
			t.Fatal("UA mismatch")
		}
	}
	h, e := scenarioHeaders(c, Case{}, Scenario{"mobile", "GB", "logged_in", "EUR"})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(h.Get("Cookie"), "currency=EUR") || !strings.Contains(h.Get("Cookie"), "wordpress_logged_in_test=abc") {
		t.Fatal(h)
	}
}
func TestSequentialCompletesBody(t *testing.T) {
	var active, max atomic.Int32
	var bytesSent atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		if n > max.Load() {
			max.Store(n)
		}
		defer active.Add(-1)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(10 * time.Millisecond)
		fmt.Fprint(w, "complete")
		bytesSent.Add(8)
	}))
	defer s.Close()
	c := testConfig()
	x := Case{Method: "GET", Requests: 8}
	p := make([]WorkItem, 8)
	for i := range p {
		p[i] = WorkItem{"/", Scenario{"desktop", "NL", "logged_out", ""}}
	}
	samples, stats := runSequential(context.Background(), c, Target{Name: "local", BaseURL: s.URL}, x, p)
	if max.Load() != 1 || bytesSent.Load() != 64 || stats.Completed != 8 || stats.Bytes != 64 {
		t.Fatal(stats, max.Load())
	}
	for _, v := range samples {
		if v.LatencyMS-v.HeadersMS < 5 {
			t.Fatal("body not timed", v)
		}
	}
}
func TestHTTPFailuresAndRedirects(t *testing.T) {
	var redirect atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			redirect.Add(1)
		}
		http.Redirect(w, r, "/next", 302)
	}))
	defer s.Close()
	c := testConfig()
	samples, stats := runSequential(context.Background(), c, Target{Name: "local", BaseURL: s.URL}, Case{Method: "GET"}, []WorkItem{{"/", Scenario{"desktop", "NL", "logged_out", ""}}})
	if redirect.Load() != 0 || stats.Errors != 1 || samples[0].Status != 302 {
		t.Fatal(samples, stats)
	}
}
func TestSitemapIndexAndPrefix(t *testing.T) {
	var base string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.xml" {
			fmt.Fprintf(w, `<sitemapindex><sitemap><loc>%s/products.xml</loc></sitemap></sitemapindex>`, base)
			return
		}
		fmt.Fprintf(w, `<urlset><url><loc>%s/product/b/</loc></url><url><loc>%s/other/</loc></url><url><loc>%s/product/a/</loc></url></urlset>`, base, base, base)
	}))
	defer s.Close()
	base = s.URL
	c := testConfig()
	c.Sitemap = SitemapConfig{URL: base + "/index.xml", PathPrefix: "/product/"}
	p, e := discoverPaths(context.Background(), c)
	if e != nil || !reflect.DeepEqual(p, []string{"/product/a/", "/product/b/"}) {
		t.Fatal(p, e)
	}
}
func TestWRKParsing(t *testing.T) {
	raw := `Running 10s test
 Latency 21.45ms 8.58ms 91.21ms 73.40%
 Req/Sec 6.67k 792.10 7.97k 78.57%
 50% 20.19ms
 75% 25.75ms
 90% 32.32ms
 99% 48.04ms
 65610 requests in 10.00s, 80.18MB read
 Socket errors: connect 1, read 2, write 3, timeout 4
 Non-2xx or 3xx responses: 5
Requests/sec: 6560.43
Transfer/sec: 8.02MB`
	w, e := parseWRK(raw)
	if e != nil || w.TotalRequests != 65610 || w.LatencyMS["p99"] != 48.04 || w.ThreadRequestsPerSecond["average"] != 6670 || w.SocketErrors["timeout"] != 4 || w.Non2xx3xx != 5 {
		t.Fatal(w, e)
	}
	if _, e = parseWRK("failed"); e == nil {
		t.Fatal("accepted failure")
	}
	n, e := scalar("900us", "time")
	if e != nil || n != .9 {
		t.Fatal(n, e)
	}
}
func TestPublishReplay(t *testing.T) {
	t.Setenv("TEST_TOKEN", "secret")
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Idempotency-Key") != "id" {
			t.Error("missing auth or idempotency")
		}
		var result Result
		if json.NewDecoder(r.Body).Decode(&result) != nil {
			t.Error("bad JSON")
		}
		count.Add(1)
		w.WriteHeader(201)
	}))
	defer s.Close()
	c := testConfig()
	c.Publish.URL = s.URL
	dir := t.TempDir()
	r := Result{SchemaVersion: 1, ID: "id"}
	if e := deliver(context.Background(), c, dir, r); e != nil {
		t.Fatal(e)
	}
	if e := replay(context.Background(), c, dir); e != nil {
		t.Fatal(e)
	}
	if count.Load() != 1 {
		t.Fatal("duplicate replay")
	}
	os.Remove(filepath.Join(dir, "result-id.json.sent"))
	if e := replay(context.Background(), c, dir); e != nil {
		t.Fatal(e)
	}
	if count.Load() != 2 {
		t.Fatal(count.Load())
	}
}
func TestRequestURLSafety(t *testing.T) {
	for _, p := range []string{"https://other.test/", "//other.test/", "relative", "/#fragment"} {
		if _, e := requestURL("https://example.test", p); e == nil {
			t.Fatal(p)
		}
	}
}

func TestSitemapGzip(t *testing.T) {
	var base string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gz := gzip.NewWriter(w)
		fmt.Fprintf(gz, `<urlset><url><loc>%s/product/one/</loc></url></urlset>`, base)
		gz.Close()
	}))
	defer s.Close()
	base = s.URL
	c := testConfig()
	c.Sitemap = SitemapConfig{URL: base + "/products.xml.gz"}
	paths, e := discoverPaths(context.Background(), c)
	if e != nil || len(paths) != 1 || paths[0] != "/product/one/" {
		t.Fatal(paths, e)
	}
}
func TestEveryProductBeforeRepeat(t *testing.T) {
	c := testConfig()
	paths := make([]string, 200)
	for i := range paths {
		paths[i] = fmt.Sprintf("/product/%d/", i)
	}
	p := buildPlan(c, Case{Kind: "sitemap", Requests: 1000}, paths)
	count := map[string]int{}
	for _, v := range p[:200] {
		count[v.Path]++
	}
	if len(count) != 200 {
		t.Fatal("not all products visited")
	}
	count = map[string]int{}
	for _, v := range p {
		count[v.Path]++
	}
	for _, n := range count {
		if n != 5 {
			t.Fatal(n)
		}
	}
}
func TestWRKSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 if [ "$1" = "-s" ]; then
  shift
  test -f "$1" || exit 1
  grep -q 'wordpress_logged_in_test' "$1" || exit 1
 fi
 shift
done
cat <<'OUTPUT'
 Latency 1ms 0ms 2ms 90%
 Req/Sec 1k 10 2k 90%
 50% 1ms
 75% 1ms
 90% 2ms
 99% 2ms
 1000 requests in 1s, 1MB read
Requests/sec: 1000
Transfer/sec: 1MB
OUTPUT
`
	if e := os.WriteFile(filepath.Join(dir, "wrk"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_LOGIN", "wordpress_logged_in_test=secret")
	c := testConfig()
	currency := "EUR"
	x := Case{Kind: "endpoint", Method: "GET", Path: "/", Requests: 3, Device: "desktop", Country: "NL", Session: "logged_in", Currency: &currency}
	w, e := runWRK(context.Background(), c, Target{Name: "test", BaseURL: "https://local.warpserver.com"}, x, WRKProfile{Threads: 1, Connections: 1, DurationSeconds: 1})
	if e != nil || w.TotalRequests != 1000 {
		t.Fatal(w, e)
	}
}
