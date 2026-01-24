package risk

import (
	"log"
	"sync"
	"time"

	"github.com/kiwhtas/deltago/internal/delta"
)

// StopLossChecker is an interface for strategies that support stop loss
type StopLossChecker interface {
	CheckStopLoss() (shouldStop bool, currentLoss float64, err error)
	ClosePosition() error
	GetActivePosition() interface{}
}

// Manager handles risk management including stop-loss monitoring
type Manager struct {
	client          *delta.Client
	wsClient        *delta.WebSocketClient
	stopLossMulti   float64
	maxDailyLoss    float64
	strategies      []StopLossChecker
	stopChan        chan struct{}
	mu              sync.Mutex
	checkMu         sync.Mutex // Prevents concurrent stop-loss checks
	stopOnce        sync.Once  // Prevents double-close panic
	dailyPnL        float64
	monitorInterval time.Duration
	isRunning       bool
}

// NewManager creates a new risk manager
func NewManager(client *delta.Client, wsURL string, stopLossMultiplier, maxDailyLoss float64) *Manager {
	return &Manager{
		client:          client,
		wsClient:        delta.NewWebSocketClient(wsURL, client.GetAuth()),
		stopLossMulti:   stopLossMultiplier,
		maxDailyLoss:    maxDailyLoss,
		strategies:      make([]StopLossChecker, 0),
		stopChan:        make(chan struct{}),
		monitorInterval: 5 * time.Second,
	}
}

// AddStrategy adds a strategy to be monitored
func (m *Manager) AddStrategy(s StopLossChecker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strategies = append(m.strategies, s)
}

// Start begins real-time risk monitoring
func (m *Manager) Start() error {
	m.mu.Lock()
	if m.isRunning {
		m.mu.Unlock()
		return nil
	}
	m.isRunning = true
	m.stopChan = make(chan struct{}) // Recreate channel for restart
	m.stopOnce = sync.Once{}         // Reset once for restart
	m.mu.Unlock()

	// Reset WebSocket client if it was previously closed
	m.wsClient.Reset()

	// Connect WebSocket for real-time updates
	if err := m.wsClient.Connect(); err != nil {
		log.Printf("Warning: WebSocket connection failed, falling back to polling: %v", err)
	} else {
		// Subscribe to position updates
		if err := m.wsClient.SubscribePositions(); err != nil {
			log.Printf("Warning: Failed to subscribe to positions: %v", err)
		}

		// Handle position updates
		m.wsClient.OnPositionUpdate(func(update delta.PositionUpdate) {
			log.Printf("Position update: %s action=%s size=%d", update.Symbol, update.Action, update.Size)
			// Trigger stop loss check on position updates
			go m.CheckAllStopLosses()
		})
	}

	// Start polling monitor as backup/primary
	go m.monitorLoop()

	return nil
}

// Stop stops the risk monitoring - safe to call multiple times
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		if !m.isRunning {
			m.mu.Unlock()
			return
		}
		m.isRunning = false
		m.mu.Unlock()

		close(m.stopChan)
		m.wsClient.Close()
	})
}

func (m *Manager) monitorLoop() {
	ticker := time.NewTicker(m.monitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.CheckAllStopLosses()
			m.CheckDailyLoss()
		}
	}
}

func (m *Manager) CheckAllStopLosses() {
	// Use trylock pattern to prevent concurrent stop-loss checks
	// This avoids double-closing positions from WS updates + polling
	if !m.checkMu.TryLock() {
		return // Another check is already in progress
	}
	defer m.checkMu.Unlock()

	m.mu.Lock()
	strategies := make([]StopLossChecker, len(m.strategies))
	copy(strategies, m.strategies)
	m.mu.Unlock()

	for _, s := range strategies {
		if s.GetActivePosition() == nil {
			continue
		}

		shouldStop, currentLoss, err := s.CheckStopLoss()
		if err != nil {
			log.Printf("Error checking stop loss: %v", err)
			continue
		}

		if shouldStop {
			log.Printf("⚠️  STOP LOSS TRIGGERED! Current loss: $%.2f - Closing position...", currentLoss)
			if err := s.ClosePosition(); err != nil {
				log.Printf("Error closing position: %v", err)
			} else {
				log.Println("Position closed successfully")
				m.mu.Lock()
				m.dailyPnL -= currentLoss
				m.mu.Unlock()
			}
		}
	}
}

func (m *Manager) CheckDailyLoss() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.dailyPnL < -m.maxDailyLoss {
		log.Printf("⛔ MAX DAILY LOSS REACHED: $%.2f - Halting all trading", m.dailyPnL)
		// Close all positions
		for _, s := range m.strategies {
			if s.GetActivePosition() != nil {
				if err := s.ClosePosition(); err != nil {
					log.Printf("Error closing position: %v", err)
				}
			}
		}
	}
}

// GetDailyPnL returns the current daily P&L
func (m *Manager) GetDailyPnL() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dailyPnL
}

// ResetDailyPnL resets the daily P&L (call at start of new trading day)
func (m *Manager) ResetDailyPnL() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dailyPnL = 0
}

// EmergencyClose closes all positions immediately
func (m *Manager) EmergencyClose() error {
	log.Println("🚨 EMERGENCY CLOSE: Closing all positions...")
	return m.client.CloseAllPositions()
}
