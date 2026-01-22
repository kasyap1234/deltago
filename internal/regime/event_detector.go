package regime

import "time"

type EventDetector struct {
	timezone *time.Location
}

func NewEventDetector() *EventDetector {
	return &EventDetector{
		timezone: time.UTC,
	}
}

func (d *EventDetector) DetectEventRisk(t time.Time) EventRisk {
	// Delta Exchange follows IST for expiry? Need to verify.
	// Assuming options expire at 5:30 PM IST (standard Indian market close)
	// But Delta is crypto, typically 8:00 AM UTC or 5:30 PM IST.
	
	// Load IST if possible, else use offset
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		ist = time.FixedZone("IST", 5*3600+1800) // UTC+5:30
	}
	
	local := t.In(ist)
	
	// Daily options expire at 5:30 PM IST
	expiryHour := 17
	expiryMinute := 30
	
	// Check if within 2 hours of expiry on any day (since crypto has daily options)
	if local.Hour() >= expiryHour-2 && local.Hour() <= expiryHour {
		if local.Hour() == expiryHour && local.Minute() > expiryMinute {
			// After expiry
			return EventRiskNone
		}
		return EventRiskExpiry
	}
	
	// Check for major data releases (NFP, CPI, FOMC) - simplified placeholder
	// In a real system, this would check a calendar
	if local.Weekday() == time.Friday && local.Hour() == 19 && local.Minute() < 30 {
		// Example: Friday 7 PM IST (common time for US data)
		return EventRiskAnnounce
	}
	
	return EventRiskNone
}
