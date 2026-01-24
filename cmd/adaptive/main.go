package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kiwhtas/deltago/internal/bot"
	"github.com/kiwhtas/deltago/internal/config"
	"github.com/kiwhtas/deltago/internal/delta"
)

func main() {
	// Command line flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	dryRun := flag.Bool("dry-run", false, "Dry run mode (no actual trades)")
	underlying := flag.String("underlying", "", "Override underlying asset (BTC, ETH)")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Println("🤖 Delta Exchange Adaptive Trading Bot")
	log.Printf("   Mode: %s", getMode(cfg.API.Testnet))

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

	// Build bot config
	botCfg := bot.DefaultConfig()
	if *underlying != "" {
		botCfg.Underlying = *underlying
	} else if len(cfg.Strategy.Underlyings) > 0 {
		botCfg.Underlying = cfg.Strategy.Underlyings[0]
	}
	botCfg.MaxDailyLoss = cfg.Risk.MaxDailyLoss
	botCfg.PositionSize = cfg.Strategy.Straddle.PositionSize
	if cfg.Strategy.IronCondor.PositionSize > 0 {
		botCfg.PositionSize = cfg.Strategy.IronCondor.PositionSize
	}
	botCfg.IronCondorDelta = cfg.Strategy.IronCondor.ShortDelta
	botCfg.IronCondorWings = cfg.Strategy.IronCondor.WingWidth
	botCfg.Testnet = cfg.API.Testnet

	// Create adaptive bot
	adaptiveBot := bot.NewAdaptiveBot(client, botCfg)

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	// Start the bot
	if !*dryRun {
		if err := adaptiveBot.Start(ctx); err != nil {
			log.Fatalf("Failed to start bot: %v", err)
		}
	} else {
		log.Println("Dry run mode - bot not started")
		// Just monitor regime in dry run
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					r := adaptiveBot.GetCurrentRegime()
					if r != nil {
						log.Printf("[DRY RUN] Current Regime: Trend=%s Vol=%s Stress=%s",
							r.Trend, r.Vol, r.Stress)
					}
				}
			}
		}()
	}

	// Print strategy info
	log.Println("\n📋 Strategy Matrix:")
	log.Println("   Uptrend     → Bull Call Spread")
	log.Println("   Downtrend   → Bear Put Spread")
	log.Println("   Sideways+HV → Iron Condor (sell premium)")
	log.Println("   Sideways+LV → Long Straddle (buy vol)")
	log.Println("   Crash       → Protective Put")

	// Wait for shutdown
	sig := <-shutdown
	log.Printf("\n🛑 Received %v, shutting down...", sig)

	// Cancel context
	cancel()

	// Stop bot
	adaptiveBot.Stop()

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
