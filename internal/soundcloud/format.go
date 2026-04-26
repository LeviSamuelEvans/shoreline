package soundcloud

import "fmt"

func FormatDuration(ms int64) string {
	if ms <= 0 {
		return "0:00"
	}

	totalSeconds := ms / 1000
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60

	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}
