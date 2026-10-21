package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Concurrency      int               `json:"concurrency"`
	ParallelTargets  bool              `json:"parallel_targets"`
	Campaign         CampaignConfig    `json:"campaign"`
	Name             string            `json:"name"`
	Seed             int64             `json:"seed"`
	Requests         int               `json:"requests"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
	CountryHeader    string            `json:"country_header"`
	Countries        []string          `json:"countries"`
	Currencies       []string          `json:"currencies"`
	CurrencyCookie   string            `json:"currency_cookie"`
	SessionCookieEnv map[string]string `json:"session_cookie_env"`
	CommonCookieEnv  string            `json:"common_cookie_env"`
	UserAgentEnv     map[string]string `json:"user_agent_env"`
	Headers          map[string]string `json:"headers"`
	Targets          []Target          `json:"targets"`
	Sitemap          SitemapConfig     `json:"sitemap"`
	Cases            []Case            `json:"cases"`
	WRK              []WRKProfile      `json:"wrk"`
	Publish          PublishConfig     `json:"publish"`
}
type Target struct {
	Repeats int    `json:"repeats,omitempty"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
}
type SitemapConfig struct {
	URL        string `json:"url"`
	PathsFile  string `json:"paths_file"`
	PathPrefix string `json:"path_prefix"`
	MaxURLs    int    `json:"max_urls"`
}
type Case struct {
	Name             string            `json:"name"`
	Kind             string            `json:"kind"`
	Path             string            `json:"path"`
	Method           string            `json:"method"`
	Body             string            `json:"body"`
	Requests         int               `json:"requests"`
	Device           string            `json:"device"`
	Country          string            `json:"country"`
	Session          string            `json:"session"`
	Currency         *string           `json:"currency"`
	Headers          map[string]string `json:"headers"`
	ExpectedStatuses []int             `json:"expected_statuses"`
	CampaignEnabled  *bool             `json:"campaign_enabled"`
	CacheMode        string            `json:"cache_mode"`
}
type WRKProfile struct {
	Name            string `json:"name"`
	Case            string `json:"case"`
	Threads         int    `json:"threads"`
	Connections     int    `json:"connections"`
	DurationSeconds int    `json:"duration_seconds"`
}
type PublishConfig struct {
	URL      string `json:"url"`
	TokenEnv string `json:"token_env"`
	Attempts int    `json:"attempts"`
}

func loadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, fmt.Errorf("configuration must contain one JSON object")
	}
	if c.Requests == 0 {
		c.Requests = 1000
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	if c.Concurrency < 1 || c.Concurrency > 20 {
		return c, fmt.Errorf("concurrency must be between 1 and 20")
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 30
	}
	if c.CurrencyCookie == "" {
		c.CurrencyCookie = "currency"
	}
	if c.Publish.TokenEnv == "" {
		c.Publish.TokenEnv = "BENCH_TOKEN"
	}
	if c.Publish.Attempts == 0 {
		c.Publish.Attempts = 3
	}
	if c.Name == "" || len(c.Targets) == 0 || len(c.Cases) == 0 {
		return c, fmt.Errorf("name, targets and cases are required")
	}
	if c.Requests < 1 || c.TimeoutSeconds < 1 || c.Publish.Attempts < 1 || c.Sitemap.MaxURLs < 0 {
		return c, fmt.Errorf("counts and timeout must be positive")
	}
	if len(c.Countries) == 0 {
		c.Countries = []string{""}
	}
	if len(c.Currencies) == 0 {
		c.Currencies = []string{""}
	}
	if c.Campaign.Probability < 0 || c.Campaign.Probability > 1 {
		return c, fmt.Errorf("campaign probability must be between 0 and 1")
	}
	for _, headers := range []map[string]string{c.Headers} {
		for k := range headers {
			if strings.EqualFold(k, "User-Agent") {
				return c, fmt.Errorf("use user_agent_env for device UAs")
			}
		}
	}
	if c.CountryHeader == "" {
		for _, v := range c.Countries {
			if v != "" {
				return c, fmt.Errorf("set country_header to the header your installation recognises")
			}
		}
	}
	seen := map[string]bool{}
	for i := range c.Targets {
		t := &c.Targets[i]
		if t.Repeats == 0 {
			t.Repeats = 1
		}
		if t.Repeats < 1 {
			return c, fmt.Errorf("target %s: repeats must be positive", t.Name)
		}
		if t.Name == "" || seen[t.Name] {
			return c, fmt.Errorf("target names must be unique")
		}
		seen[t.Name] = true
		if err := validateURL(t.BaseURL); err != nil {
			return c, err
		}
		u, _ := url.Parse(t.BaseURL)
		if u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
			return c, fmt.Errorf("base_url must be an origin: %s", t.Name)
		}
	}
	if c.Sitemap.URL != "" {
		if err := validateURL(c.Sitemap.URL); err != nil {
			return c, err
		}
	}
	if c.Publish.URL != "" {
		if err := validateURL(c.Publish.URL); err != nil {
			return c, err
		}
		if os.Getenv(c.Publish.TokenEnv) == "" {
			return c, fmt.Errorf("missing environment variable %s", c.Publish.TokenEnv)
		}
	}
	for k, v := range c.SessionCookieEnv {
		if k != "logged_in" && k != "logged_out" {
			return c, fmt.Errorf("unknown session %s", k)
		}
		if v != "" && os.Getenv(v) == "" {
			return c, fmt.Errorf("missing environment variable %s", v)
		}
	}
	names := map[string]bool{}
	for i := range c.Cases {
		x := &c.Cases[i]
		for k := range x.Headers {
			if strings.EqualFold(k, "User-Agent") {
				return c, fmt.Errorf("use user_agent_env for device UAs")
			}
		}
		if x.Name == "" || names[x.Name] {
			return c, fmt.Errorf("case names must be unique")
		}
		names[x.Name] = true
		if x.Kind != "sitemap" && x.Kind != "endpoint" {
			return c, fmt.Errorf("case %s: kind must be sitemap or endpoint", x.Name)
		}
		if x.Method == "" {
			x.Method = "GET"
		}
		if x.Requests == 0 {
			x.Requests = c.Requests
		}
		if x.Requests < 1 {
			return c, fmt.Errorf("invalid request count")
		}
		if x.Kind == "endpoint" {
			if _, err := requestURL("https://example.test", x.Path); err != nil {
				return c, err
			}
		}
		if x.Kind == "sitemap" && c.Sitemap.URL == "" && c.Sitemap.PathsFile == "" {
			return c, fmt.Errorf("sitemap URL or paths_file required")
		}
		if x.Device != "" && x.Device != "desktop" && x.Device != "mobile" && x.Device != "tablet" {
			return c, fmt.Errorf("invalid device")
		}
		if x.Session != "" && x.Session != "logged_in" && x.Session != "logged_out" {
			return c, fmt.Errorf("invalid session")
		}
		if x.Session == "logged_in" && os.Getenv(c.SessionCookieEnv["logged_in"]) == "" {
			return c, fmt.Errorf("logged_in case requires a login cookie environment variable")
		}
		for _, s := range x.ExpectedStatuses {
			if s < 100 || s > 599 {
				return c, fmt.Errorf("invalid expected status")
			}
		}
	}
	for _, p := range c.WRK {
		if !names[p.Case] || p.Threads < 1 || p.Connections < p.Threads || p.DurationSeconds < 1 {
			return c, fmt.Errorf("invalid wrk profile %s", p.Name)
		}
		for _, x := range c.Cases {
			if x.Name == p.Case && (x.Kind != "endpoint" || x.Device == "" || x.Session == "" || x.Currency == nil || x.Country == "") {
				return c, fmt.Errorf("wrk case %s must be endpoint with fixed device, country, session and currency", x.Name)
			}
		}
	}
	return c, nil
}
func validateURL(s string) error {
	u, e := url.Parse(s)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid HTTP URL")
	}
	return nil
}
func requestURL(base, path string) (string, error) {
	u, e := url.Parse(path)
	if e != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || u.Fragment != "" {
		return "", fmt.Errorf("request paths must start with / and contain no host or fragment")
	}
	b, e := url.Parse(base)
	if e != nil {
		return "", e
	}
	return b.ResolveReference(u).String(), nil
}
func newClient(seconds int) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true
	tr.MaxIdleConnsPerHost = 1
	return &http.Client{Transport: tr, Timeout: time.Duration(seconds) * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}
