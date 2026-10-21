package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	b = append(b, '\n')
	tmp, e := os.CreateTemp(filepath.Dir(path), ".result-*")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, e = tmp.Write(b); e != nil {
		tmp.Close()
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}
func publish(ctx context.Context, c Config, r Result) error {
	if c.Publish.URL == "" {
		return nil
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	client := newClient(c.TimeoutSeconds)
	defer client.CloseIdleConnections()
	for attempt := 0; attempt < c.Publish.Attempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		req, e := http.NewRequestWithContext(ctx, "POST", c.Publish.URL, bytes.NewReader(b))
		if e != nil {
			return fmt.Errorf("invalid publisher request")
		}
		req.Header.Set("Authorization", "Bearer "+os.Getenv(c.Publish.TokenEnv))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", r.ID)
		resp, e := client.Do(req)
		if e != nil {
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode != 429 && resp.StatusCode < 500 {
			return fmt.Errorf("publisher HTTP %d", resp.StatusCode)
		}
	}
	return fmt.Errorf("publisher unavailable after %d attempts", c.Publish.Attempts)
}
func deliver(ctx context.Context, c Config, dir string, r Result) error {
	path := filepath.Join(dir, "result-"+r.ID+".json")
	if e := writeJSON(path, r); e != nil {
		return e
	}
	if c.Publish.URL == "" {
		return nil
	}
	if e := publish(ctx, c, r); e != nil {
		return e
	}
	return os.WriteFile(path+".sent", []byte(time.Now().UTC().Format(time.RFC3339)), 0600)
}
func replay(ctx context.Context, c Config, dir string) error {
	if c.Publish.URL == "" {
		return fmt.Errorf("publish URL required for replay")
	}
	paths, e := filepath.Glob(filepath.Join(dir, "result-*.json"))
	if e != nil {
		return e
	}
	for _, path := range paths {
		if _, e := os.Stat(path + ".sent"); e == nil {
			continue
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var r Result
		if e = json.Unmarshal(b, &r); e != nil {
			return e
		}
		if r.SchemaVersion == 0 {
			continue
		}
		if e = publish(ctx, c, r); e != nil {
			return e
		}
		if e = os.WriteFile(path+".sent", []byte(time.Now().UTC().Format(time.RFC3339)), 0600); e != nil {
			return e
		}
		fmt.Printf("Published %s\n", r.ID)
	}
	return nil
}
