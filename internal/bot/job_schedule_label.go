package bot

import "strings"

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
