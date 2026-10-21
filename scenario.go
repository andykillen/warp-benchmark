package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
)

type Device struct{ UA, Platform, Mobile string }

var devices = map[string]Device{
	"desktop": {"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36", "Linux", "?0"},
	"mobile":  {"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36", "Android", "?1"},
	"tablet":  {"Mozilla/5.0 (Linux; Android 14; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36", "Android", "?0"},
}

type Scenario struct {
	Device   string `json:"device"`
	Country  string `json:"country"`
	Session  string `json:"session"`
	Currency string `json:"currency"`
}
type WorkItem struct {
	Path     string   `json:"path"`
	Scenario Scenario `json:"scenario"`
}

// Build a balanced Cartesian matrix, then shuffle deterministically. Each complete
// cycle contains every path/state combination once; repeated cycles are explicit.
func buildPlan(c Config, x Case, paths []string) []WorkItem {
	if x.Kind == "endpoint" {
		paths = []string{x.Path}
	}
	ds := []string{"desktop", "mobile", "tablet"}
	if x.Device != "" {
		ds = []string{x.Device}
	}
	countries := c.Countries
	if x.Country != "" {
		countries = []string{x.Country}
	}
	ss := []string{"logged_out"}
	if c.SessionCookieEnv["logged_in"] != "" {
		ss = append(ss, "logged_in")
	}
	if x.Session != "" {
		ss = []string{x.Session}
	}
	currencies := c.Currencies
	if x.Currency != nil {
		currencies = []string{*x.Currency}
	}
	// Shuffle states independently, then round-robin them so a 1,000-request subset
	// has balanced state coverage, even when the full matrix is much larger.
	states := []Scenario{}
	for _, d := range ds {
		for _, co := range countries {
			for _, s := range ss {
				for _, cu := range currencies {
					states = append(states, Scenario{d, co, s, cu})
				}
			}
		}
	}
	rng := rand.New(rand.NewSource(c.Seed))
	rng.Shuffle(len(states), func(i, j int) { states[i], states[j] = states[j], states[i] })
	// Each path is visited before any path repeats. Rotate state positions
	// between path passes so all path/state pairs are covered over a full matrix.
	order := append([]string(nil), paths...)
	rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })
	plan := make([]WorkItem, x.Requests)
	for i := range plan {
		pass := i / len(order)
		position := i % len(order)
		si := (position + pass) % len(states)
		plan[i] = WorkItem{order[position], states[si]}
	}

	return plan
}
func scenarioHeaders(c Config, x Case, s Scenario) (http.Header, error) {
	d := devices[s.Device]
	if v := os.Getenv(c.UserAgentEnv[s.Device]); v != "" {
		d.UA = v
	}
	h := http.Header{}
	h.Set("User-Agent", d.UA)
	h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	h.Set("Accept-Encoding", "gzip")
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("Sec-Fetch-Mode", "navigate")
	h.Set("Sec-Fetch-Site", "none")
	h.Set("Sec-Fetch-User", "?1")
	h.Set("Upgrade-Insecure-Requests", "1")
	// Custom UAs may carry different Chrome versions; avoid inconsistent hints.
	if os.Getenv(c.UserAgentEnv[s.Device]) == "" {
		h.Set("Sec-CH-UA", `"Chromium";v="131", "Google Chrome";v="131", "Not_A Brand";v="24"`)
		h.Set("Sec-CH-UA-Mobile", d.Mobile)
		h.Set("Sec-CH-UA-Platform", `"`+d.Platform+`"`)
	}
	for k, v := range c.Headers {
		if v == "" {
			h.Del(k)
		} else {
			h.Set(k, v)
		}
	}
	for k, v := range x.Headers {
		if v == "" {
			h.Del(k)
		} else {
			h.Set(k, v)
		}
	}
	if c.CountryHeader != "" {
		h.Set(c.CountryHeader, s.Country)
	}
	// Cookie is exclusively built from scenario inputs, including an explicit blank
	// logged-out environment. No cookie jar and no adoption of Set-Cookie responses.
	cookies := map[string]string{}
	for _, env := range []string{c.CommonCookieEnv, c.SessionCookieEnv[s.Session]} {
		if env == "" {
			continue
		}
		raw := os.Getenv(env)
		r := http.Request{Header: http.Header{"Cookie": []string{raw}}}
		for _, v := range r.Cookies() {
			cookies[v.Name] = v.Value
		}
	}
	delete(cookies, c.CurrencyCookie)
	if s.Currency != "" {
		cookies[c.CurrencyCookie] = s.Currency
	}
	h.Del("Cookie")
	keys := []string{}
	for k := range cookies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cookie := &http.Cookie{Name: k, Value: cookies[k]}
		if e := cookie.Valid(); e != nil {
			return nil, fmt.Errorf("invalid configured cookie")
		}
		raw := cookie.String()
		if h.Get("Cookie") == "" {
			h.Set("Cookie", raw)
		} else {
			h.Set("Cookie", h.Get("Cookie")+"; "+raw)
		}
	}
	for k, vs := range h {
		if strings.ContainsAny(k, "\r\n") {
			return nil, fmt.Errorf("invalid header name")
		}
		for _, v := range vs {
			if strings.ContainsAny(v, "\r\n") {
				return nil, fmt.Errorf("invalid header value")
			}
		}
	}
	return h, nil
}
