package delta

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestSignRequest(t *testing.T) {
	apiKey := "my_key"
	apiSecret := "my_secret"
	auth := NewAuth(apiKey, apiSecret)

	method := "POST"
	path := "/orders"
	query := "limit=10"
	body := `{"size": 100}`

	headers := auth.SignRequest(method, path, query, body)

	// Check headers existence
	if headers["api-key"] != apiKey {
		t.Errorf("Expected api-key %s, got %s", apiKey, headers["api-key"])
	}
	if headers["timestamp"] == "" {
		t.Error("Timestamp missing")
	}
	if headers["signature"] == "" {
		t.Error("Signature missing")
	}

	// Verify signature content (reconstruct message with captured timestamp)
	timestamp := headers["timestamp"]
	expectedMessage := method + timestamp + path + "?" + query + body
	
	h := hmac.New(sha256.New, []byte(apiSecret))
	h.Write([]byte(expectedMessage))
	expectedSignature := hex.EncodeToString(h.Sum(nil))

	if headers["signature"] != expectedSignature {
		t.Errorf("Signature mismatch. Expected %s, got %s", expectedSignature, headers["signature"])
	}
}

func TestSignWebSocket(t *testing.T) {
	auth := NewAuth("key", "secret")
	authData := auth.SignWebSocket()

	if authData["type"] != "key-auth" {
		t.Errorf("Expected type key-auth, got %v", authData["type"])
	}

	payload, ok := authData["payload"].(map[string]string)
	if !ok {
		t.Fatal("Payload is not map[string]string")
	}

	if payload["api-key"] != "key" {
		t.Errorf("Expected api-key key, got %s", payload["api-key"])
	}
	
	// Verify signature for WS (GET + timestamp + /live)
	timestamp := payload["timestamp"]
	expectedMessage := "GET" + timestamp + "/live"
	
	h := hmac.New(sha256.New, []byte("secret"))
	h.Write([]byte(expectedMessage))
	expectedSignature := hex.EncodeToString(h.Sum(nil))
	
	if payload["signature"] != expectedSignature {
		t.Errorf("WS Signature mismatch. Expected %s, got %s", expectedSignature, payload["signature"])
	}
}
