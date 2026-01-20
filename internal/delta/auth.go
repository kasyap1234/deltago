package delta

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// Auth handles Delta Exchange API authentication
type Auth struct {
	apiKey    string
	apiSecret string
}

// NewAuth creates a new Auth instance
func NewAuth(apiKey, apiSecret string) *Auth {
	return &Auth{
		apiKey:    apiKey,
		apiSecret: apiSecret,
	}
}

// SignRequest generates authentication headers for a request
// The signature is: HMAC_SHA256(secret, method + timestamp + path + query + body)
func (a *Auth) SignRequest(method, path, query, body string) map[string]string {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	// Build the message to sign
	// Format: METHOD + TIMESTAMP + PATH + QUERY + BODY
	message := method + timestamp + path
	if query != "" {
		message += "?" + query
	}
	message += body

	// Generate HMAC-SHA256 signature
	signature := a.generateSignature(message)

	return map[string]string{
		"api-key":      a.apiKey,
		"timestamp":    timestamp,
		"signature":    signature,
		"User-Agent":   "deltago-trading-bot/1.0",
		"Content-Type": "application/json",
	}
}

// generateSignature creates HMAC-SHA256 signature
func (a *Auth) generateSignature(message string) string {
	h := hmac.New(sha256.New, []byte(a.apiSecret))
	h.Write([]byte(message))
	return hex.EncodeToString(h.Sum(nil))
}

// SignWebSocket generates authentication payload for WebSocket connection
func (a *Auth) SignWebSocket() map[string]interface{} {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	method := "GET"
	path := "/live"

	message := method + timestamp + path
	signature := a.generateSignature(message)

	return map[string]interface{}{
		"type": "auth",
		"payload": map[string]string{
			"api-key":   a.apiKey,
			"timestamp": timestamp,
			"signature": signature,
		},
	}
}

// ValidateCredentials checks if API credentials are set
func (a *Auth) ValidateCredentials() error {
	if a.apiKey == "" {
		return fmt.Errorf("API key is not set")
	}
	if a.apiSecret == "" {
		return fmt.Errorf("API secret is not set")
	}
	return nil
}
