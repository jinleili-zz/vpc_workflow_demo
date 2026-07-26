package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSuccessfulAZWriteResponsesExposeSagaSuccessCode(t *testing.T) {
	t.Parallel()

	tests := map[string]any{
		"vpc":    VPCResponse{Success: true, Message: "accepted"},
		"subnet": SubnetResponse{Success: true, Message: "accepted"},
		"pccn":   PCCNResponse{Success: true, Message: "accepted"},
		"vfw":    AZFirewallPolicyResponse{Success: true, Message: "accepted"},
	}

	for name, response := range tests {
		name, response := name, response
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			payload, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}

			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if got := body["code"]; got != "0" {
				t.Fatalf("code = %#v, want %q; payload=%s", got, "0", payload)
			}
		})
	}
}

func TestAZWriteResponsesExposeCommonOperationFields(t *testing.T) {
	t.Parallel()

	tests := map[string]any{
		"vpc":    VPCResponse{},
		"subnet": SubnetResponse{},
		"pccn":   PCCNResponse{},
		"vfw":    AZFirewallPolicyResponse{},
	}
	want := map[string]string{
		"OperationID": "operation_id",
		"ResourceID":  "resource_id",
		"Status":      "status",
	}

	for name, response := range tests {
		name, response := name, response
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			typ := reflect.TypeOf(response)
			for fieldName, jsonName := range want {
				field, ok := typ.FieldByName(fieldName)
				if !ok {
					t.Fatalf("%s missing field %s", typ.Name(), fieldName)
				}
				if got := field.Tag.Get("json"); got != jsonName+",omitempty" {
					t.Fatalf("%s.%s json tag = %q, want %q", typ.Name(), fieldName, got, jsonName+",omitempty")
				}
			}
		})
	}
}
