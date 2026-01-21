package backtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var coinGeckoIDs = map[string]string{
	"BTC":     "bitcoin",
	"bitcoin": "bitcoin",
	"ETH":     "ethereum",
	"eth":     "ethereum",
}

type DataFetcher struct {
	CacheDir   string
	HTTPClient *http.Client
}

func NewDataFetcher(cacheDir string) *DataFetcher {
	if cacheDir == "" {
		cacheDir = ".backtest_cache"
	}
	return &DataFetcher{
		CacheDir: cacheDir,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (f *DataFetcher) FetchOHLCV(underlying string, days int) ([]OHLCV, error) {
	cachedData, err := f.loadFromCache(underlying, days)
	if err == nil && len(cachedData) > 0 {
		return cachedData, nil
	}

	data, err := f.fetchFromAPI(underlying, days)
	if err != nil {
		return nil, err
	}

	if err := f.saveToCache(underlying, days, data); err != nil {
		fmt.Printf("warning: failed to cache data: %v\n", err)
	}

	return data, nil
}

func (f *DataFetcher) fetchFromAPI(underlying string, days int) ([]OHLCV, error) {
	coinID, ok := coinGeckoIDs[underlying]
	if !ok {
		return nil, fmt.Errorf("unknown underlying: %s", underlying)
	}

	// Use market_chart endpoint for daily data (better than OHLC which is 4-hourly)
	url := fmt.Sprintf(
		"https://api.coingecko.com/api/v3/coins/%s/market_chart?vs_currency=usd&days=%d&interval=daily",
		coinID, days,
	)

	resp, err := f.HTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// market_chart returns {prices: [[ts, price], ...], ...}
	var chartData struct {
		Prices [][]float64 `json:"prices"`
	}
	if err := json.Unmarshal(body, &chartData); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return parseMarketChartData(chartData.Prices), nil
}

// parseMarketChartData converts market_chart prices to OHLCV
// Since market_chart only gives prices (not OHLC), we simulate high/low
func parseMarketChartData(prices [][]float64) []OHLCV {
	var result []OHLCV
	for i, point := range prices {
		if len(point) < 2 {
			continue
		}
		price := point[1]
		
		// Simulate realistic high/low based on typical daily range (~2-4% for BTC)
		// Use previous day's price to estimate movement
		var high, low float64
		if i > 0 && len(prices[i-1]) >= 2 {
			prevPrice := prices[i-1][1]
			move := (price - prevPrice) / prevPrice
			// Estimate intraday range based on daily move
			rangeEstimate := 0.015 + 0.5*absFloat(move) // Base 1.5% + half the actual move
			if move >= 0 {
				high = price * (1 + rangeEstimate*0.3)
				low = price * (1 - rangeEstimate*0.7)
			} else {
				high = price * (1 + rangeEstimate*0.7)
				low = price * (1 - rangeEstimate*0.3)
			}
		} else {
			high = price * 1.015
			low = price * 0.985
		}
		
		result = append(result, OHLCV{
			Timestamp: time.UnixMilli(int64(point[0])),
			Open:      price,
			High:      high,
			Low:       low,
			Close:     price,
			Volume:    0,
		})
	}
	return result
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func parseOHLCVData(rawData [][]float64) []OHLCV {
	var result []OHLCV
	for _, candle := range rawData {
		if len(candle) < 5 {
			continue
		}
		result = append(result, OHLCV{
			Timestamp: time.UnixMilli(int64(candle[0])),
			Open:      candle[1],
			High:      candle[2],
			Low:       candle[3],
			Close:     candle[4],
			Volume:    0,
		})
	}
	return result
}

func (f *DataFetcher) cacheFilePath(underlying string, days int) string {
	filename := fmt.Sprintf("%s_%d_days.json", underlying, days)
	return filepath.Join(f.CacheDir, filename)
}

type cachedData struct {
	FetchedAt time.Time `json:"fetched_at"`
	Data      []OHLCV   `json:"data"`
}

func (f *DataFetcher) loadFromCache(underlying string, days int) ([]OHLCV, error) {
	path := f.cacheFilePath(underlying, days)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cached cachedData
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}

	if time.Since(cached.FetchedAt) > 24*time.Hour {
		return nil, fmt.Errorf("cache expired")
	}

	return cached.Data, nil
}

func (f *DataFetcher) saveToCache(underlying string, days int, data []OHLCV) error {
	if err := os.MkdirAll(f.CacheDir, 0755); err != nil {
		return err
	}

	cached := cachedData{
		FetchedAt: time.Now(),
		Data:      data,
	}

	jsonData, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(f.cacheFilePath(underlying, days), jsonData, 0644)
}

func (f *DataFetcher) ClearCache() error {
	return os.RemoveAll(f.CacheDir)
}

func (f *DataFetcher) FilterByDateRange(data []OHLCV, start, end time.Time) []OHLCV {
	var filtered []OHLCV
	for _, candle := range data {
		if (candle.Timestamp.Equal(start) || candle.Timestamp.After(start)) &&
			(candle.Timestamp.Equal(end) || candle.Timestamp.Before(end)) {
			filtered = append(filtered, candle)
		}
	}
	return filtered
}
