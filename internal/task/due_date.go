package task

import (
	"time"
	_ "time/tzdata"
)

// ValidDueDate accepts only a real ISO civil date. It deliberately rejects
// timestamps, whitespace, and PostgreSQL's special infinity values.
func ValidDueDate(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Year() >= 1 && parsed.Year() <= 9999 && parsed.Format("2006-01-02") == value
}

func ValidTimeZone(value string) bool {
	if len(value) == 0 || len(value) > 100 || value == "Local" {
		return false
	}
	_, err := time.LoadLocation(value)
	return err == nil
}
