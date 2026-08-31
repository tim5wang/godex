package textutil

// TruncateRunes limits value to limit Unicode code points and appends an
// ellipsis when truncated. Non-positive limits leave value unchanged.
func TruncateRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
