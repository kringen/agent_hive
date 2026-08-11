// Package health provides simple liveness/readiness HTTP handlers for
// container orchestration (Docker/K8s) health checks.
package health

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// Checker reports whether a dependency is currently healthy. Implementations
// should be cheap and non-blocking (e.g. a ping or an atomic flag read) since
// they may be called frequently by an orchestrator.
type Checker func() error

// Server tracks named readiness checks and serves /healthz and /readyz.
type Server struct {
	ready  atomic.Bool
	checks map[string]Checker
}

// NewServer returns a Server that starts as not-ready until MarkReady is
// called (typically once startup dependencies like NATS/DB are connected).
func NewServer() *Server {
	return &Server{checks: make(map[string]Checker)}
}

// AddCheck registers a named readiness check. All registered checks must
// pass for /readyz to return 200.
func (s *Server) AddCheck(name string, check Checker) {
	s.checks[name] = check
}

// MarkReady flips the overall readiness flag on. Use this once initial
// startup (DB connections, NATS connect, stream/consumer setup) succeeds.
func (s *Server) MarkReady() {
	s.ready.Store(true)
}

// MarkNotReady flips readiness off, e.g. if a critical dependency is lost.
func (s *Server) MarkNotReady() {
	s.ready.Store(false)
}

// LivezHandler always returns 200 as long as the process is running and
// able to handle HTTP requests — it does not check dependencies. Use this
// for container restart decisions (a failing liveness check means "kill and
// restart me"), as opposed to readiness (which means "don't route me
// traffic/work yet").
func (s *Server) LivezHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ReadyzHandler returns 200 only if MarkReady has been called and every
// registered check currently passes; otherwise 503 with a per-check
// breakdown so `curl` output is actually useful for debugging.
func (s *Server) ReadyzHandler(w http.ResponseWriter, r *http.Request) {
	results := make(map[string]string, len(s.checks))
	ok := s.ready.Load()

	for name, check := range s.checks {
		if err := check(); err != nil {
			results[name] = err.Error()
			ok = false
		} else {
			results[name] = "ok"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ready":  s.ready.Load(),
		"checks": results,
	})
}
