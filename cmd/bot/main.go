package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kiwhtas/deltago/internal/config"
	"github.com/kiwhtas/deltago/internal/delta"
	"github.com/kiwhtas/deltago/internal/risk"
	"github.com/kiwhtas/deltago/internal/strategy"
)

func main() {
	// Command line flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	dryRun := flag.Bool("dry-run", false, "Dry run mode (no actual trades)")
	forceEntry := flag.Bool("force-entry", false, "Force immediate entry (ignore scheduled time)")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Println("🚀 Delta Exchange India Trading Bot")
	log.Printf("   Mode: %s", getMode(cfg.API.Testnet))
	log.Printf("   Underlyings: %v", cfg.Strategy.Underlyings)
	log.Printf("   Straddle Enabled: %t", cfg.Strategy.Straddle.Enabled)
	log.Printf("   Iron Condor Enabled: %t", cfg.Strategy.IronCondor.Enabled)
	log.Printf("   Stop Loss Multiplier: %.2fx", cfg.Risk.StopLossMultiplier)

	if *dryRun {
		log.Println("   ⚠️  DRY RUN MODE - No trades will be executed")
	}

	// Create API client
	var client *delta.Client
	if cfg.API.Testnet {
		client = delta.NewTestnetClient(cfg.API.APIKey, cfg.API.APISecret)
	} else {
		client = delta.NewProductionClient(cfg.API.APIKey, cfg.API.APISecret)
	}

	// Verify connectivity
	if err := verifyConnectivity(client); err != nil {
		log.Fatalf("Failed to connect to Delta Exchange: %v", err)
	}
	log.Println("✅ Connected to Delta Exchange")

	// Create risk manager
	riskMgr := risk.NewManager(
		client,
		cfg.API.WebSocketURL,
		cfg.Risk.StopLossMultiplier,
		cfg.Risk.MaxDailyLoss,
	)

	// Create strategies for each underlying
	var straddles []*strategy.Straddle
	var condors []*strategy.IronCondor

	for _, underlying := range cfg.Strategy.Underlyings {
		if cfg.Strategy.Straddle.Enabled {
			s := strategy.NewStraddle(
				client,
				underlying,
				cfg.Strategy.Straddle.PositionSize,
				cfg.Risk.StopLossMultiplier,
			)
			straddles = append(straddles, s)
			riskMgr.AddStrategy(s)
			log.Printf("   Straddle strategy registered for %s", underlying)
		}

		if cfg.Strategy.IronCondor.Enabled {
			ic := strategy.NewIronCondor(
				client,
				underlying,
				cfg.Strategy.IronCondor.PositionSize,
				cfg.Strategy.IronCondor.ShortDelta,
				cfg.Strategy.IronCondor.WingWidth,
				cfg.Risk.StopLossMultiplier,
			)
			condors = append(condors, ic)
			riskMgr.AddStrategy(ic)
			log.Printf("   Iron Condor strategy registered for %s", underlying)
		}
	}

	// Start risk monitoring
	if !*dryRun {
		if err := riskMgr.Start(); err != nil {
			log.Printf("Warning: Risk monitoring start failed: %v", err)
		}
		log.Println("✅ Risk monitoring started")
	}

	// Handle graceful shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	// Entry scheduling
	if *forceEntry {
		log.Println("⚡ Force entry mode - executing strategies now...")
		executeStrategies(straddles, condors, *dryRun)
	} else if cfg.Strategy.Straddle.Enabled {
		// Schedule entry at configured time
		go scheduleEntry(cfg, straddles, condors, *dryRun)
	}

	// Wait for shutdown signal
	<-shutdown
	log.Println("\n🛑 Shutting down...")

	// Stop risk monitoring
	riskMgr.Stop()

	log.Println("Goodbye!")
}

func verifyConnectivity(client *delta.Client) error {
	_, err := client.GetUSDBalance()
	return err
}

func getMode(testnet bool) string {
	if testnet {
		return "TESTNET"
	}
	return "PRODUCTION"
}

func executeStrategies(straddles []*strategy.Straddle, condors []*strategy.IronCondor, dryRun bool) {
	for _, s := range straddles {
		if dryRun {
			log.Printf("[DRY RUN] Would execute straddle for underlying")
			continue
		}
		pos, err := s.Execute()
		if err != nil {
			log.Printf("Error executing straddle: %v", err)
		} else {
			log.Printf("✅ Straddle opened: %s/%s, premium: $%.2f, max loss: $%.2f",
				pos.CallSymbol, pos.PutSymbol, pos.TotalPremium, pos.MaxLoss)
		}
	}

	for _, ic := range condors {
		if dryRun {
			log.Printf("[DRY RUN] Would execute iron condor for underlying")
			continue
		}
		pos, err := ic.Execute()
		if err != nil {
			log.Printf("Error executing iron condor: %v", err)
		} else {
			log.Printf("✅ Iron Condor opened: net credit: $%.2f, max loss: $%.2f",
				pos.NetCredit, pos.MaxLoss)
		}
	}
}

func scheduleEntry(cfg *config.Config, straddles []*strategy.Straddle, condors []*strategy.IronCondor, dryRun bool) {
	entryTime, err := cfg.Strategy.Straddle.GetEntryTime()
	if err != nil {
		log.Printf("Error parsing entry time: %v", err)
		return
	}

	// If entry time has passed today, schedule for tomorrow
	now := time.Now()
	if now.After(entryTime) {
		entryTime = entryTime.Add(24 * time.Hour)
		log.Printf("Entry time passed for today, scheduling for tomorrow: %s", entryTime.Format(time.RFC3339))
	}

	waitDuration := time.Until(entryTime)
	log.Printf("⏰ Scheduled entry at %s (%v from now)", entryTime.Format("15:04 IST"), waitDuration.Round(time.Minute))

	timer := time.NewTimer(waitDuration)
	<-timer.C

	log.Println("⚡ Entry time reached - executing strategies...")
	executeStrategies(straddles, condors, dryRun)
}
