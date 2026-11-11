package agent

import (
	"os"
	"strings"
	"time"
)

// Name local time using an actual zone source, never by guessing from an offset.
func runtimeTimezone(now time.Time) string {
	name := now.Location().String()
	if name != "Local" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
		return now.Format("-07:00") + " (offset only; IANA zone unavailable)"
	}
	candidates := []string{strings.TrimPrefix(os.Getenv("TZ"), ":")}
	if path, err := os.Readlink("/etc/localtime"); err == nil {
		candidates = append(candidates, zoneNameFromPath(path))
	}
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(data)))
	}
	_, offset := now.Zone()
	for _, candidate := range candidates {
		if candidate == "" || candidate == "Local" {
			continue
		}
		if zone, err := time.LoadLocation(candidate); err == nil {
			_, actual := now.In(zone).Zone()
			if actual == offset {
				return candidate
			}
		}
	}
	return now.Format("-07:00") + " (offset only; IANA zone unavailable)"
}

func zoneNameFromPath(path string) string {
	_, name, found := strings.Cut(path, "/zoneinfo/")
	if !found {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(name, "posix/"), "right/")
}

func schedulingTimezone(configured, local string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" || configured == "Local" {
		return local
	}
	return configured
}
