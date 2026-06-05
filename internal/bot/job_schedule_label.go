package bot

import (
	"strings"
	"time"
)

func jobScheduleLabel(cronSchedule string) string {
	normalized := strings.Join(strings.Fields(cronSchedule), " ")
	switch normalized {
	case "0 * * * *":
		return "Every hour"
	case "0 */4 * * *":
		return "Every 4 hours"
	case "0 */6 * * *":
		return "Every 6 hours"
	case "0 */8 * * *":
		return "Every 8 hours"
	case "0 */12 * * *":
		return "Every 12 hours"
	case "0 9 * * *":
		return "Daily"
	case "0 9 * * 1":
		return "Weekly"
	default:
		return strings.TrimSpace(cronSchedule)
	}
}

func jobTimezoneLabel(timezone string) string {
	tz := strings.TrimSpace(timezone)
	if tz == "" || strings.EqualFold(tz, "UTC") {
		return "UTC"
	}
	parts := strings.Split(tz, "/")
	if len(parts) > 1 {
		name := strings.ReplaceAll(parts[len(parts)-1], "_", " ")
		if name != "" {
			return name + " time"
		}
	}
	return tz
}

func jobDisplayTime(t time.Time, timezone string) string {
	if t.IsZero() {
		return ""
	}
	loc := time.UTC
	if tz := strings.TrimSpace(timezone); tz != "" {
		if loaded, err := time.LoadLocation(tz); err == nil {
			loc = loaded
		}
	}
	return t.In(loc).Format("Jan 2 at 3:04 PM")
}
