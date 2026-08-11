// Command healthcheck is a minimal binary for use in Docker HEALTHCHECK
// directives on distroless images (no shell, no curl/wget available).
// It exits 0 if the target URL returns 2xx, 1 otherwise.
package main

import (
	"net/http"
	"os"
	"time"
)

func main() {
	url := "http://localhost:8080/readyz"
	if v := os.Getenv("HEALTHCHECK_URL"); v != "" {
		url = v
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		os.Exit(1)
	}
}
