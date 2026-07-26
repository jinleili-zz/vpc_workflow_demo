package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jinleili-zz/nsp-platform/auth"
	"github.com/jinleili-zz/nsp-platform/trace"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCreateSubnetPropagatesOperationIdentity(t *testing.T) {
	client := NewAZNSPClient(nil, nil)
	client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		for header, want := range map[string]string{
			operation.HeaderSagaTransactionID:  "saga-1",
			operation.HeaderIdempotencyKey:     "child-key-1",
			operation.HeaderRootOperationID:    "root-1",
			operation.HeaderParentOperationID:  "parent-1",
			operation.HeaderResourceGeneration: "3",
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("%s=%q, want %q", header, got, want)
			}
		}
		body, _ := json.Marshal(models.SubnetResponse{Success: true})
		return jsonResponse(http.StatusOK, string(body)), nil
	})}
	ctx := operation.ContextWithIdentity(context.Background(), operation.RequestIdentity{
		SagaTransactionID: "saga-1", IdempotencyKey: "child-key-1",
		RootOperationID: "root-1", ParentOperationID: "parent-1", ResourceGeneration: 3,
	})
	if _, err := client.CreateSubnet(ctx, "http://example.com", &models.SubnetRequest{SubnetName: "subnet-a", VPCName: "vpc-a", Region: "region-a", AZ: "az-a", CIDR: "10.0.0.0/24"}); err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestAZNSPClientWithTrace(t *testing.T) {
	tracedClient := trace.NewTracedClient(nil)
	client := NewAZNSPClientWithTrace(tracedClient, tracedClient.Client(), nil)

	if client.tracedClient == nil {
		t.Fatal("tracedClient should not be nil")
	}
}

func TestAZNSPClientWithoutTrace(t *testing.T) {
	client := NewAZNSPClient(nil, nil)

	if client.tracedClient != nil {
		t.Fatal("tracedClient should be nil for plain client")
	}
	if client.httpClient == nil {
		t.Fatal("httpClient should not be nil")
	}
}

func TestCreateVPCWithTrace(t *testing.T) {
	tracedClient := trace.NewTracedClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
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
			body, _ := json.Marshal(resp)
			return jsonResponse(http.StatusOK, string(body)), nil
		}),
	})
	client := NewAZNSPClientWithTrace(tracedClient, tracedClient.Client(), nil)

	// Create trace context
	tc := &trace.TraceContext{
		TraceID:    "test-create-vpc-trace-id",
		SpanId:     "test-span-id",
		InstanceId: "test-instance",
		Sampled:    true,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	req := &models.VPCRequest{VPCName: "test-vpc", Region: "region-1"}
	resp, err := client.CreateVPC(ctx, "http://example.com", req)
	if err != nil {
		t.Fatalf("CreateVPC failed: %v", err)
	}
	if !resp.Success {
		t.Errorf("Expected success, got: %s", resp.Message)
	}
}

func TestDeleteVPCWithTrace(t *testing.T) {
	tracedClient := trace.NewTracedClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			// Verify trace headers
			traceID := r.Header.Get("X-B3-TraceId")
			if traceID == "" {
				t.Error("Expected X-B3-TraceId header in request")
			}

			if r.Method != "DELETE" {
				t.Errorf("Expected DELETE, got %s", r.Method)
			}

			return jsonResponse(http.StatusNoContent, ""), nil
		}),
	})
	client := NewAZNSPClientWithTrace(tracedClient, tracedClient.Client(), nil)

	tc := &trace.TraceContext{
		TraceID:    "test-delete-vpc-trace-id",
		SpanId:     "test-span-id",
		InstanceId: "test-instance",
		Sampled:    true,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	err := client.DeleteVPC(ctx, "http://example.com", "test-vpc")
	if err != nil {
		t.Fatalf("DeleteVPC failed: %v", err)
	}
}

func TestDeleteVPCWithoutTrace(t *testing.T) {
	client := NewAZNSPClient(nil, nil)
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != "DELETE" {
				t.Errorf("Expected DELETE, got %s", r.Method)
			}
			return jsonResponse(http.StatusOK, ""), nil
		}),
	}

	err := client.DeleteVPC(context.Background(), "http://example.com", "test-vpc")
	if err != nil {
		t.Fatalf("DeleteVPC without trace failed: %v", err)
	}
}

func TestDeleteVPCTreatsNotFoundAsAlreadyAbsent(t *testing.T) {
	client := NewAZNSPClient(nil, nil)
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusNotFound, `{"message":"VPC not found"}`), nil
		}),
	}

	if err := client.DeleteVPC(context.Background(), "http://example.com", "test-vpc"); err != nil {
		t.Fatalf("DeleteVPC should treat a missing VPC as already absent: %v", err)
	}
}

func TestDeletePCCNTreatsNotFoundAsAlreadyAbsent(t *testing.T) {
	client := NewAZNSPClient(nil, nil)
	client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/pccn/pccn-a" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		return jsonResponse(http.StatusNotFound, ""), nil
	})}
	if err := client.DeletePCCN(t.Context(), "http://example.com", "pccn-a"); err != nil {
		t.Fatalf("DeletePCCN should ensure absent: %v", err)
	}
}

func TestHealthCheckWithTrace(t *testing.T) {
	tracedClient := trace.NewTracedClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			traceID := r.Header.Get("X-B3-TraceId")
			if traceID == "" {
				t.Error("Expected X-B3-TraceId header")
			}
			if r.Method != "GET" {
				t.Errorf("Expected GET, got %s", r.Method)
			}
			return jsonResponse(http.StatusOK, ""), nil
		}),
	})
	client := NewAZNSPClientWithTrace(tracedClient, tracedClient.Client(), nil)

	tc := &trace.TraceContext{
		TraceID:    "test-health-trace-id",
		SpanId:     "test-span-id",
		InstanceId: "test-instance",
		Sampled:    true,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	err := client.HealthCheck(ctx, "http://example.com")
	if err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}
}

func TestCreateVPCWithSigner(t *testing.T) {
	client := NewAZNSPClient(nil, auth.NewSigner("top-nsp", "test-secret-key-12345"))
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") == "" {
				t.Error("Expected Authorization header in request")
			}
			if r.Header.Get(auth.HeaderTimestamp) == "" {
				t.Error("Expected signed timestamp header in request")
			}
			if r.Header.Get(auth.HeaderNonce) == "" {
				t.Error("Expected signed nonce header in request")
			}

			resp := models.VPCResponse{Success: true, Message: "VPC created"}
			body, _ := json.Marshal(resp)
			return jsonResponse(http.StatusOK, string(body)), nil
		}),
	}

	req := &models.VPCRequest{VPCName: "test-vpc", Region: "region-1"}
	resp, err := client.CreateVPC(context.Background(), "http://example.com", req)
	if err != nil {
		t.Fatalf("CreateVPC failed: %v", err)
	}
	if !resp.Success {
		t.Errorf("Expected success, got: %s", resp.Message)
	}
}

func TestAZNSPClientUsesInjectedHTTPClient(t *testing.T) {
	injected := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp := models.VPCResponse{Success: true, Message: "ok"}
			body, _ := json.Marshal(resp)
			return jsonResponse(http.StatusOK, string(body)), nil
		}),
	}

	client := NewAZNSPClient(injected, nil)
	if client.httpClient != injected {
		t.Fatalf("httpClient was not preserved")
	}
}
