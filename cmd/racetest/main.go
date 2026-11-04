// Command racetest hammers a running control plane with concurrent
// same-slug creates and same-version PATCHes and prints a summary of the
// status codes. It exits non-zero if more than one request won either race.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

func main() {
	apiURL := flag.String("url", envOr("API_URL", "http://localhost:8000"), "control plane base URL [API_URL]")
	n := flag.Int("n", 20, "concurrent requests per race")
	flag.Parse()

	if err := run(*apiURL, *n); err != nil {
		fmt.Fprintln(os.Stderr, "racetest:", err)
		os.Exit(1)
	}
}

type client struct {
	base string
	http *http.Client
}

type result struct {
	status int
	code   string
}

func run(base string, n int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := &client{base: base, http: &http.Client{Timeout: 10 * time.Second}}
	slug := "race-" + uuid.NewString()[:8]

	fmt.Printf("Race 1: %d concurrent POST /tenants with slug %q\n", n, slug)
	creates := race(n, func() result {
		res, _ := c.do(ctx, http.MethodPost, "/tenants", map[string]any{"slug": slug, "name": "Race test"})
		return res
	})
	printSummary(creates)

	var tenantID string
	for _, r := range creates {
		if r.status == http.StatusCreated {
			tenantID = r.code // the id is stored in code for winners
		}
	}
	if tenantID == "" {
		return errors.New("no create succeeded")
	}

	fmt.Printf("\nWaiting for tenant %s to become active (needs the worker)...\n", tenantID)
	version, err := c.waitActive(ctx, tenantID)
	if err != nil {
		return err
	}

	fmt.Printf("\nRace 2: %d concurrent PATCH /tenants/%s with version %d\n", n, tenantID, version)
	patches := race(n, func() result {
		res, _ := c.do(ctx, http.MethodPatch, "/tenants/"+tenantID, map[string]any{"name": "Race winner", "version": version})
		return res
	})
	printSummary(patches)

	if wins := count(creates, http.StatusCreated); wins != 1 {
		return fmt.Errorf("expected exactly 1 successful create, got %d", wins)
	}
	if wins := count(patches, http.StatusAccepted); wins != 1 {
		return fmt.Errorf("expected exactly 1 successful PATCH, got %d", wins)
	}
	fmt.Println("\nOK: exactly one winner in each race.")
	return nil
}

// race runs fn n times concurrently, releasing all goroutines at once.
func race(n int, fn func() result) []result {
	results := make([]result, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = fn()
		}()
	}
	close(start)
	wg.Wait()
	return results
}

// do sends a JSON request. For 2xx responses result.code holds the
// returned id; otherwise it holds the error code.
func (c *client) do(ctx context.Context, method, path string, body any) (result, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, &buf)
	if err != nil {
		return result{code: err.Error()}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return result{code: "request failed: " + err.Error()}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	res := result{status: resp.StatusCode}
	if resp.StatusCode < 300 {
		res.code, _ = out["id"].(string)
	} else if e, ok := out["error"].(map[string]any); ok {
		res.code, _ = e["code"].(string)
	}
	return res, out
}

func (c *client) waitActive(ctx context.Context, id string) (int, error) {
	for {
		res, body := c.do(ctx, http.MethodGet, "/tenants/"+id, nil)
		if res.status == http.StatusOK && body["status"] == "active" {
			v, _ := body["version"].(float64)
			return int(v), nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("tenant never became active (last status %v): %w", body["status"], ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func printSummary(results []result) {
	counts := map[string]int{}
	for _, r := range results {
		label := fmt.Sprintf("%d", r.status)
		if r.status >= 300 || r.status == 0 {
			label += " " + r.code
		}
		counts[label]++
	}
	labels := make([]string, 0, len(counts))
	for l := range counts {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		fmt.Printf("  %-40s x%d\n", l, counts[l])
	}
}

func count(results []result, status int) int {
	n := 0
	for _, r := range results {
		if r.status == status {
			n++
		}
	}
	return n
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
