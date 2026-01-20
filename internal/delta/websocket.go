package delta

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocketClient handles real-time data from Delta Exchange
type WebSocketClient struct {
	url         string
	auth        *Auth
	conn        *websocket.Conn
	done        chan struct{}
	reconnect   bool
	mu          sync.Mutex
	handlers    map[string][]func(json.RawMessage)
	isConnected bool
	pingTicker  *time.Ticker
}

// PositionUpdate represents a position update from WebSocket
type PositionUpdate struct {
	Type          string `json:"type"`
	Action        string `json:"action"` // "create", "update", "delete"
	Symbol        string `json:"symbol"`
	ProductID     int64  `json:"product_id"`
	Size          int    `json:"size"`
	Margin        string `json:"margin"`
	EntryPrice    string `json:"entry_price"`
	Commission    string `json:"commission"`
	UnrealizedPnL string `json:"unrealized_pnl,omitempty"`
}

// NewWebSocketClient creates a new WebSocket client
func NewWebSocketClient(wsURL string, auth *Auth) *WebSocketClient {
	return &WebSocketClient{
		url:       wsURL,
		auth:      auth,
		done:      make(chan struct{}),
		reconnect: true,
		handlers:  make(map[string][]func(json.RawMessage)),
	}
}

// Connect establishes WebSocket connection and authenticates
func (w *WebSocketClient) Connect() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	conn, _, err := websocket.DefaultDialer.Dial(w.url, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}

	w.conn = conn
	w.isConnected = true

	// Authenticate
	authMsg := w.auth.SignWebSocket()
	if err := conn.WriteJSON(authMsg); err != nil {
		return fmt.Errorf("failed to send auth message: %w", err)
	}

	// Start ping ticker to keep connection alive
	w.pingTicker = time.NewTicker(30 * time.Second)
	go w.keepAlive()

	// Start message reader
	go w.readMessages()

	return nil
}

// Subscribe subscribes to a channel
func (w *WebSocketClient) Subscribe(channel string, symbols []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.isConnected {
		return fmt.Errorf("WebSocket not connected")
	}

	msg := map[string]interface{}{
		"type": "subscribe",
		"payload": map[string]interface{}{
			"channels": []map[string]interface{}{
				{
					"name":    channel,
					"symbols": symbols,
				},
			},
		},
	}

	return w.conn.WriteJSON(msg)
}

// SubscribePositions subscribes to position updates for all symbols
func (w *WebSocketClient) SubscribePositions() error {
	return w.Subscribe("positions", []string{"all"})
}

// SubscribeTicker subscribes to ticker updates for specific symbols
func (w *WebSocketClient) SubscribeTicker(symbols []string) error {
	return w.Subscribe("v2/ticker", symbols)
}

// OnPositionUpdate registers a handler for position updates
func (w *WebSocketClient) OnPositionUpdate(handler func(PositionUpdate)) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.handlers["positions"] = append(w.handlers["positions"], func(data json.RawMessage) {
		var update PositionUpdate
		if err := json.Unmarshal(data, &update); err != nil {
			log.Printf("Failed to parse position update: %v", err)
			return
		}
		handler(update)
	})
}

// OnTicker registers a handler for ticker updates
func (w *WebSocketClient) OnTicker(handler func(Ticker)) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.handlers["v2/ticker"] = append(w.handlers["v2/ticker"], func(data json.RawMessage) {
		var ticker Ticker
		if err := json.Unmarshal(data, &ticker); err != nil {
			log.Printf("Failed to parse ticker: %v", err)
			return
		}
		handler(ticker)
	})
}

func (w *WebSocketClient) readMessages() {
	defer func() {
		w.mu.Lock()
		w.isConnected = false
		w.mu.Unlock()

		if w.reconnect {
			log.Println("WebSocket disconnected, attempting reconnect...")
			time.Sleep(5 * time.Second)
			if err := w.Connect(); err != nil {
				log.Printf("Reconnection failed: %v", err)
			}
		}
	}()

	for {
		select {
		case <-w.done:
			return
		default:
			_, message, err := w.conn.ReadMessage()
			if err != nil {
				log.Printf("WebSocket read error: %v", err)
				return
			}

			w.handleMessage(message)
		}
	}
}

func (w *WebSocketClient) handleMessage(message []byte) {
	var msg struct {
		Type    string          `json:"type"`
		Success bool            `json:"success,omitempty"`
		Result  json.RawMessage `json:"result,omitempty"`
	}

	if err := json.Unmarshal(message, &msg); err != nil {
		log.Printf("Failed to parse WebSocket message: %v", err)
		return
	}

	// Handle different message types
	switch msg.Type {
	case "auth":
		if msg.Success {
			log.Println("WebSocket authenticated successfully")
		} else {
			log.Println("WebSocket authentication failed")
		}
	case "subscriptions":
		log.Printf("Subscription confirmed: %s", string(message))
	case "positions":
		w.dispatchHandlers("positions", message)
	case "v2/ticker":
		w.dispatchHandlers("v2/ticker", message)
	default:
		// For other message types, try to dispatch based on type
		w.dispatchHandlers(msg.Type, message)
	}
}

func (w *WebSocketClient) dispatchHandlers(eventType string, data json.RawMessage) {
	w.mu.Lock()
	handlers := w.handlers[eventType]
	w.mu.Unlock()

	for _, handler := range handlers {
		go handler(data)
	}
}

func (w *WebSocketClient) keepAlive() {
	for {
		select {
		case <-w.done:
			w.pingTicker.Stop()
			return
		case <-w.pingTicker.C:
			w.mu.Lock()
			if w.isConnected && w.conn != nil {
				if err := w.conn.WriteJSON(map[string]string{"type": "ping"}); err != nil {
					log.Printf("Failed to send ping: %v", err)
				}
			}
			w.mu.Unlock()
		}
	}
}

// Close closes the WebSocket connection
func (w *WebSocketClient) Close() error {
	w.reconnect = false
	close(w.done)

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.conn != nil {
		return w.conn.Close()
	}
	return nil
}

// IsConnected returns whether the WebSocket is connected
func (w *WebSocketClient) IsConnected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.isConnected
}
