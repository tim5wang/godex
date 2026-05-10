package textutil

import "unicode"

func Title(value string) string {
	runes := []rune(value)
	upperNext := true
	for i, r := range runes {
		if upperNext && unicode.IsLetter(r) {
			runes[i] = unicode.ToTitle(r)
			upperNext = false
			continue
		}
		upperNext = !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	}
	return string(runes)
}
