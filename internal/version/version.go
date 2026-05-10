package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// These variables are intended to be overridden by release builds with -ldflags,
// for example: -X github.com/tim5wang/godex/internal/version.Version=v0.1.0.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func Current() Info {
	info := Info{
		Version:   clean(Version, "dev"),
		Commit:    strings.TrimSpace(Commit),
		Date:      strings.TrimSpace(Date),
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		if info.Commit == "" {
			info.Commit = buildSetting(buildInfo, "vcs.revision")
		}
		if info.Date == "" {
			info.Date = buildSetting(buildInfo, "vcs.time")
		}
	}
	return info
}

func Summary() string {
	info := Current()
	parts := []string{"godex " + info.Version}
	if info.Commit != "" {
		commit := info.Commit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		parts = append(parts, "commit "+commit)
	}
	if info.Date != "" {
		parts = append(parts, "built "+info.Date)
	}
	parts = append(parts, fmt.Sprintf("%s/%s", info.OS, info.Arch), info.GoVersion)
	return strings.Join(parts, " ")
}

func buildSetting(info *debug.BuildInfo, key string) string {
	if info == nil {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == key {
			return strings.TrimSpace(setting.Value)
		}
	}
	return ""
}

func clean(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
