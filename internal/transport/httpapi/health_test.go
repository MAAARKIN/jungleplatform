package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maaarkin/jungleplatform/internal/transport/httpapi"
)

func TestLiveAlwaysOK(t *testing.T) {
	srv := httptest.NewServer(httpapi.NewHealthHandler(func(context.Context) error { return nil }).Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/live")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("live status = %d, want 200", resp.StatusCode)
	}
}

func TestReadyOKWhenDependenciesUp(t *testing.T) {
	srv := httptest.NewServer(httpapi.NewHealthHandler(func(context.Context) error { return nil }).Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/ready")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("ready status = %d, want 200", resp.StatusCode)
	}
}

func TestReadyServiceUnavailableWhenCheckFails(t *testing.T) {
	srv := httptest.NewServer(httpapi.NewHealthHandler(
		func(context.Context) error { return errors.New("postgres down") },
	).Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/ready")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("ready status = %d, want 503", resp.StatusCode)
	}
}
