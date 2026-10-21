package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

// Sitemaps are discovered once, before timing. Paths are replayed on every target.
func discoverPaths(ctx context.Context, c Config) ([]string, error) {
	found := map[string]bool{}
	add := func(p string) error {
		if _, err := requestURL("https://example.test", p); err != nil {
			return err
		}
		if c.Sitemap.PathPrefix == "" || strings.HasPrefix(p, c.Sitemap.PathPrefix) {
			found[p] = true
		}
		return nil
	}
	if c.Sitemap.PathsFile != "" {
		f, e := os.Open(c.Sitemap.PathsFile)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 4096), 1024*1024)
		for s.Scan() {
			p := strings.TrimSpace(s.Text())
			if p != "" && !strings.HasPrefix(p, "#") {
				if e := add(p); e != nil {
					return nil, e
				}
			}
		}
		if s.Err() != nil {
			return nil, s.Err()
		}
	} else {
		client := newClient(c.TimeoutSeconds)
		defer client.CloseIdleConnections()
		seen := map[string]bool{}
		root, _ := url.Parse(c.Sitemap.URL)
		var visit func(string, int) error
		visit = func(location string, depth int) error {
			if depth > 16 || len(seen) >= 1000 {
				return fmt.Errorf("sitemap recursion limit exceeded")
			}
			if seen[location] {
				return nil
			}
			seen[location] = true
			u, e := url.Parse(location)
			if e != nil || u.Scheme != root.Scheme || u.Host != root.Host {
				return fmt.Errorf("nested sitemap must use original origin")
			}
			req, e := http.NewRequestWithContext(ctx, "GET", location, nil)
			if e != nil {
				return e
			}
			req.Header.Set("Accept", "application/xml,text/xml")
			resp, e := client.Do(req)
			if e != nil {
				return fmt.Errorf("sitemap fetch failed")
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("sitemap HTTP %d", resp.StatusCode)
			}
			var reader io.Reader = resp.Body
			if strings.HasSuffix(u.Path, ".gz") || resp.Header.Get("Content-Encoding") == "gzip" {
				gz, e := gzip.NewReader(reader)
				if e != nil {
					return e
				}
				defer gz.Close()
				reader = gz
			}
			data, e := io.ReadAll(io.LimitReader(reader, 32*1024*1024+1))
			if e != nil {
				return e
			}
			if len(data) > 32*1024*1024 {
				return fmt.Errorf("sitemap exceeds 32 MiB")
			}
			var doc struct {
				XMLName xml.Name
				URLs    []struct {
					Loc string `xml:"loc"`
				} `xml:"url"`
				Maps []struct {
					Loc string `xml:"loc"`
				} `xml:"sitemap"`
			}
			if e = xml.Unmarshal(data, &doc); e != nil {
				return e
			}
			switch doc.XMLName.Local {
			case "sitemapindex":
				for _, m := range doc.Maps {
					v, e := url.Parse(strings.TrimSpace(m.Loc))
					if e != nil {
						return e
					}
					if e = visit(u.ResolveReference(v).String(), depth+1); e != nil {
						return e
					}
				}
			case "urlset":
				for _, v := range doc.URLs {
					p, e := url.Parse(strings.TrimSpace(v.Loc))
					if e != nil {
						return e
					}
					p = u.ResolveReference(p)
					if p.Host != root.Host || p.Scheme != root.Scheme {
						return fmt.Errorf("product URL outside sitemap origin")
					}
					if e = add(p.RequestURI()); e != nil {
						return e
					}
				}
			default:
				return fmt.Errorf("unsupported sitemap root %s", doc.XMLName.Local)
			}
			return nil
		}
		if e := visit(c.Sitemap.URL, 0); e != nil {
			return nil, e
		}
	}
	paths := make([]string, 0, len(found))
	for p := range found {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if c.Sitemap.MaxURLs > 0 && len(paths) > c.Sitemap.MaxURLs {
		paths = paths[:c.Sitemap.MaxURLs]
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no matching sitemap paths")
	}
	return paths, nil
}
