package operation

import (
	"bytes"
	"testing"
)

func TestCanonicalHashIgnoresObjectKeyOrder(t *testing.T) {
	hashA, payloadA, err := CanonicalHash(1, "region-a/vpc-a", []byte(`{"vlan_id":101,"vpc_name":"vpc-a"}`))
	if err != nil {
		t.Fatalf("CanonicalHash A: %v", err)
	}
	hashB, payloadB, err := CanonicalHash(1, "region-a/vpc-a", []byte(`{"vpc_name":"vpc-a","vlan_id":101}`))
	if err != nil {
		t.Fatalf("CanonicalHash B: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("object key order changed hash: %s != %s", hashA, hashB)
	}
	if !bytes.Equal(payloadA, payloadB) {
		t.Fatalf("canonical payload differs: %s != %s", payloadA, payloadB)
	}
}

func TestCanonicalHashPreservesArrayOrder(t *testing.T) {
	hashA, _, err := CanonicalHash(1, "policy-a", map[string]any{"rules": []string{"allow-a", "deny-b"}})
	if err != nil {
		t.Fatalf("CanonicalHash A: %v", err)
	}
	hashB, _, err := CanonicalHash(1, "policy-a", map[string]any{"rules": []string{"deny-b", "allow-a"}})
	if err != nil {
		t.Fatalf("CanonicalHash B: %v", err)
	}
	if hashA == hashB {
		t.Fatal("array order did not affect hash")
	}
}

func TestCanonicalHashIncludesTargetScope(t *testing.T) {
	payload := map[string]any{"vpc_name": "vpc-a"}
	hashA, _, err := CanonicalHash(1, "region-a/vpc-a", payload)
	if err != nil {
		t.Fatalf("CanonicalHash A: %v", err)
	}
	hashB, _, err := CanonicalHash(1, "region-b/vpc-a", payload)
	if err != nil {
		t.Fatalf("CanonicalHash B: %v", err)
	}
	if hashA == hashB {
		t.Fatal("target scope did not affect hash")
	}
}

func TestCanonicalHashNormalizesEquivalentJSONNumbers(t *testing.T) {
	var expectedHash string
	var expectedPayload []byte
	for _, raw := range [][]byte{
		[]byte(`{"value":1}`),
		[]byte(`{"value":1.0}`),
		[]byte(`{"value":1e0}`),
		[]byte(`{"value":10e-1}`),
	} {
		hash, payload, err := CanonicalHash(1, "target", raw)
		if err != nil {
			t.Fatalf("CanonicalHash(%s): %v", raw, err)
		}
		if expectedHash == "" {
			expectedHash = hash
			expectedPayload = payload
			continue
		}
		if hash != expectedHash || !bytes.Equal(payload, expectedPayload) {
			t.Fatalf("equivalent number %s produced hash/payload %s/%s, want %s/%s", raw, hash, payload, expectedHash, expectedPayload)
		}
	}
}

func TestCanonicalHashRedactsCredentialFields(t *testing.T) {
	payloadA := map[string]any{
		"vpc_name": "vpc-a",
		"password": "first-password",
		"nested": map[string]any{
			"secret_key": "first-secret",
			"api_key":    "first-api-key",
			"items": []any{
				map[string]any{
					"client_secret":     "first-client-secret",
					"access_token":      "first-access-token",
					"private_key":       "first-private-key",
					"secret_access_key": "first-secret-access-key",
					"value":             "kept",
				},
			},
		},
	}
	payloadB := map[string]any{
		"vpc_name": "vpc-a",
		"password": "second-password",
		"nested": map[string]any{
			"secret_key": "second-secret",
			"api_key":    "second-api-key",
			"items": []any{
				map[string]any{
					"client_secret":     "second-client-secret",
					"access_token":      "second-access-token",
					"private_key":       "second-private-key",
					"secret_access_key": "second-secret-access-key",
					"value":             "kept",
				},
			},
		},
	}
	hashA, canonical, err := CanonicalHash(1, "region-a/vpc-a", payloadA)
	if err != nil {
		t.Fatalf("CanonicalHash A: %v", err)
	}
	hashB, _, err := CanonicalHash(1, "region-a/vpc-a", payloadB)
	if err != nil {
		t.Fatalf("CanonicalHash B: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("credential values changed hash: %s != %s", hashA, hashB)
	}
	for _, forbidden := range [][]byte{
		[]byte("password"), []byte("secret"), []byte("api_key"),
		[]byte("access_token"), []byte("private_key"),
	} {
		if bytes.Contains(canonical, forbidden) {
			t.Fatalf("canonical payload retained credential field %q: %s", forbidden, canonical)
		}
	}
	if !bytes.Contains(canonical, []byte(`"value":"kept"`)) {
		t.Fatalf("canonical payload removed non-secret data: %s", canonical)
	}
}

func TestCanonicalHashRejectsUnsupportedVersion(t *testing.T) {
	if _, _, err := CanonicalHash(2, "target", map[string]any{"value": 1}); err == nil {
		t.Fatal("unsupported hash version accepted")
	}
}
