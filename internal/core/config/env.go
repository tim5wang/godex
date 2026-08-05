package config

import (
	"github.com/joho/godotenv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func resolvePath(workspace, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(workspace, value))
}

func resolveLogPath(homeDir, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return filepath.Join(homeDir, "log", "godex.log")
	}
	value = expandHomePath(value)
	if filepath.IsAbs(value) {
		clean := filepath.Clean(value)
		if clean == filepath.Join(homeDir, "godex.log") {
			return filepath.Join(homeDir, "log", "godex.log")
		}
		return clean
	}
	clean := filepath.Clean(value)
	if clean == "godex.log" {
		return filepath.Join(homeDir, "log", "godex.log")
	}
	return filepath.Clean(filepath.Join(homeDir, clean))
}

func expandHomePath(value string) string {
	if value == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
		return value
	}
	if strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	return value
}

func readDotEnvFile(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]string{}, nil
	}
	values, err := godotenv.Read(path)
	if err != nil {
		return map[string]string{}, err
	}
	return values, nil
}

func mergeEnvMaps(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		for key, value := range layer {
			out[key] = value
		}
	}
	return out
}

func updateEnvVar(content, key, value string) string {
	prefix := key + "="
	if strings.TrimSpace(content) == "" {
		return prefix + value + "\n"
	}
	lines := strings.Split(content, "\n")
	found := false
	for idx, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[idx] = prefix + value
			found = true
		}
	}
	if !found {
		if strings.TrimSpace(content) != "" && !strings.HasSuffix(content, "\n") {
			lines = append(lines, "")
		}
		lines = append(lines, prefix+value)
	}
	return normalizeEnvContent(strings.Join(lines, "\n"))
}

func removeEnvVar(content, key string) string {
	prefix := key + "="
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			continue
		}
		out = append(out, line)
	}
	return normalizeEnvContent(strings.Join(out, "\n"))
}

func normalizeEnvContent(content string) string {
	content = strings.TrimRight(content, "\n")
	if strings.TrimSpace(content) == "" {
		return ""
	}
	return content + "\n"
}

func lookupEnvValue(values map[string]string, name string) envValue {
	if value, ok := values[name]; ok {
		return envValue{Value: value, Name: name, Set: true}
	}
	return envValue{}
}

func lookupProcessValue(name string) envValue {
	if value, ok := os.LookupEnv(name); ok {
		return envValue{Value: value, Name: name, Set: true}
	}
	return envValue{}
}

func lookupBool(values map[string]string, name string) (bool, bool) {
	value, ok := values[name]
	if !ok {
		return false, false
	}
	parsed, ok := parseBool(value)
	return parsed, ok
}

func lookupProcessBool(name string) (bool, bool) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return false, false
	}
	parsed, ok := parseBool(value)
	return parsed, ok
}

func lookupInt(values map[string]string, name string) (int, bool, string) {
	match := lookupEnvValue(values, name)
	if !match.Set {
		return 0, false, ""
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(match.Value))
	if err != nil {
		return 0, false, ""
	}
	return parsed, true, match.Name
}

func lookupProcessInt(name string) (int, bool, string) {
	match := lookupProcessValue(name)
	if !match.Set {
		return 0, false, ""
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(match.Value))
	if err != nil {
		return 0, false, ""
	}
	return parsed, true, match.Name
}

func lookupCSV(values map[string]string, name string) ([]string, bool) {
	value, ok := values[name]
	if !ok {
		return nil, false
	}
	return parseCSV(value), true
}

func lookupProcessCSV(name string) ([]string, bool) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, false
	}
	return parseCSV(value), true
}

func parseBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func parseCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
