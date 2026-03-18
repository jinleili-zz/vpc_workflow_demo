package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jinleili-zz/nsp-platform/trace"
	"workflow_qoder/internal/models"
)

func TestAZNSPClientWithTrace(t *testing.T) {
	tracedClient := trace.NewTracedClient(nil)
	client := NewAZNSPClientWithTrace(tracedClient)

	if client.tracedClient == nil {
		t.Fatal("tracedClient should not be nil")
	}
}

func TestAZNSPClientWithoutTrace(t *testing.T) {
	client := NewAZNSPClient()

	if client.tracedClient != nil {
		t.Fatal("tracedClient should be nil for plain client")
	}
	if client.httpClient == nil {
		t.Fatal("httpClient should not be nil")
	}
}

func TestCreateVPCWithTrace(t *testing.T) {
	// Mock server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify trace headers are present
		traceID := r.Header.Get("X-B3-TraceId")
		if traceID == "" {
			t.Error("Expected X-B3-TraceId header in request")
		}

		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/vpc" {
			t.Errorf("Expected /api/v1/vpc, got %s", r.URL.Path)
		}

		resp := models.VPCResponse{Success: true, Message: "VPC created"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	tracedClient := trace.NewTracedClient(nil)
	client := NewAZNSPClientWithTrace(tracedClient)

	// Create trace context
	tc := &trace.TraceContext{
		TraceID:    "test-create-vpc-trace-id",
		SpanId:     "test-span-id",
		InstanceId: "test-instance",
		Sampled:    true,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	req := &models.VPCRequest{VPCName: "test-vpc", Region: "region-1"}
	resp, err := client.CreateVPC(ctx, ts.URL, req)
	if err != nil {
		t.Fatalf("CreateVPC failed: %v", err)
	}
	if !resp.Success {
		t.Errorf("Expected success, got: %s", resp.Message)
	}
}

func TestDeleteVPCWithTrace(t *testing.T) {
	// Mock server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify trace headers
		traceID := r.Header.Get("X-B3-TraceId")
		if traceID == "" {
			t.Error("Expected X-B3-TraceId header in request")
		}

		if r.Method != "DELETE" {
			t.Errorf("Expected DELETE, got %s", r.Method)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	tracedClient := trace.NewTracedClient(nil)
	client := NewAZNSPClientWithTrace(tracedClient)

	tc := &trace.TraceContext{
		TraceID:    "test-delete-vpc-trace-id",
		SpanId:     "test-span-id",
		InstanceId: "test-instance",
		Sampled:    true,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	err := client.DeleteVPC(ctx, ts.URL, "test-vpc")
	if err != nil {
		t.Fatalf("DeleteVPC failed: %v", err)
	}
}

func TestDeleteVPCWithoutTrace(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("Expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewAZNSPClient()

	err := client.DeleteVPC(context.Background(), ts.URL, "test-vpc")
	if err != nil {
		t.Fatalf("DeleteVPC without trace failed: %v", err)
	}
}

func TestHealthCheckWithTrace(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-B3-TraceId")
		if traceID == "" {
			t.Error("Expected X-B3-TraceId header")
		}
		if r.Method != "GET" {
			t.Errorf("Expected GET, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tracedClient := trace.NewTracedClient(nil)
	client := NewAZNSPClientWithTrace(tracedClient)

	tc := &trace.TraceContext{
		TraceID:    "test-health-trace-id",
		SpanId:     "test-span-id",
		InstanceId: "test-instance",
		Sampled:    true,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	err := client.HealthCheck(ctx, ts.URL)
	if err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}
}
