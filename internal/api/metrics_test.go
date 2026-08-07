// internal/api/metrics_test.go
package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func metricsConfig(token string) config.Config {
	cfg := testConfig()
	cfg.MetricsToken = token
	return cfg
}

func getMetrics(t *testing.T, cfg config.Config, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	srv, _, _ := api.NewServer(nil, cfg)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// The endpoint has no unauthenticated shape: without METRICS_TOKEN there is no
// token that opens it, so a default install cannot leak the job inventory.
func TestMetrics_DisabledWhenNoTokenConfigured(t *testing.T) {
	assert.Equal(t, http.StatusUnauthorized, getMetrics(t, metricsConfig(""), "").Code)
	assert.Equal(t, http.StatusUnauthorized,
		getMetrics(t, metricsConfig(""), "Bearer anything").Code)
	// Not even the empty bearer that a naive constant-time compare would accept.
	assert.Equal(t, http.StatusUnauthorized, getMetrics(t, metricsConfig(""), "Bearer ").Code)
}

func TestMetrics_WrongTokenRejected(t *testing.T) {
	w := getMetrics(t, metricsConfig("s3cret"), "Bearer nope")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.NotContains(t, w.Body.String(), "nat_backup_build_info")
}

func TestMetrics_ValidTokenServesExposition(t *testing.T) {
	api.Version = "v9.9.9-test"
	w := getMetrics(t, metricsConfig("s3cret"), "Bearer s3cret")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/plain")
	assert.Contains(t, w.Body.String(), `nat_backup_build_info{version="v9.9.9-test"} 1`)
}

// A nil database must degrade to fewer series, never to a failed scrape: a 500 takes
// the target down in Prometheus and hides the metrics that would still be correct.
func TestMetrics_NilDBStillServes(t *testing.T) {
	w := getMetrics(t, metricsConfig("s3cret"), "Bearer s3cret")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "nat_backup_build_info")
}

func TestReadyz_ReportsUnavailableWithoutDB(t *testing.T) {
	srv, _, _ := api.NewServer(nil, testConfig())
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "unavailable")
}

// Liveness must stay independent of the database: an orchestrator that kills the
// process on a Postgres outage converts a recoverable dependency failure into a
// crash loop.
func TestHealthz_StaysOKWithoutDB(t *testing.T) {
	srv, _, _ := api.NewServer(nil, testConfig())
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
