package main

import (
	"kops/metric_collector"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Healthy endpoint test
func TestCollectServiceHealth_Healthy(t *testing.T) {
	// Mock server returning 200 OK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	os.Setenv("CUSTOMER_HEALTH_URL", server.URL)
	defer os.Unsetenv("CUSTOMER_HEALTH_URL")

	result := metric_collector.CollectServiceHealth()

	if !result.Healthy {
		t.Errorf("Expected healthy, got unhealthy: %v", result.ErrorMessage)
	}
}

// Unhealthy endpoint test (HTTP 500)
func TestCollectServiceHealth_Unhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	os.Setenv("CUSTOMER_HEALTH_URL", server.URL)
	defer os.Unsetenv("CUSTOMER_HEALTH_URL")

	result := metric_collector.CollectServiceHealth()

	if result.Healthy {
		t.Errorf("Expected unhealthy, got healthy")
	}
}

// Timeout simulation (delay longer than client timeout)
func TestCollectServiceHealth_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(6 * time.Second) // exceed 5s timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	os.Setenv("CUSTOMER_HEALTH_URL", server.URL)
	defer os.Unsetenv("CUSTOMER_HEALTH_URL")

	result := metric_collector.CollectServiceHealth()

	if result.Healthy {
		t.Errorf("Expected timeout failure, got healthy")
	}
}

// No URL configured
func TestCollectServiceHealth_NoURL(t *testing.T) {
	os.Unsetenv("CUSTOMER_HEALTH_URL")

	result := metric_collector.CollectServiceHealth()

	if !result.Healthy || result.ErrorMessage != "No health URL configured" {
		t.Errorf("Expected healthy with note about missing URL")
	}
}
