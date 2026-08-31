// Package valueutil converts loosely typed protocol values at UI boundaries.
package valueutil

import (
	"fmt"
	"strconv"
)

// Int extracts an integer from Go and JSON number representations.
func Int(value interface{}) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case float64:
		return int(number), true
	case fmt.Stringer:
		parsed, err := strconv.Atoi(number.String())
		return parsed, err == nil
	default:
		return 0, false
	}
}
