package retrieval

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var protectedLiteral = regexp.MustCompile("[0-9]+(?:[.][0-9]+)*[A-Za-z%秒分时天]*|`[^`]*`|[A-Za-z][A-Za-z0-9_./:-]*")

// Selection never edits or archives stored records. Cross-applicability or
// type differences, changed literals and negations prevent approximate collapse.
func selectDistinct(docs []Document, scored []scoredDocument, limit int, trace *Diagnostics) []scoredDocument {
	kept := make([]scoredDocument, 0, len(scored))
	exactSeen, formatSeen := map[duplicateKey]string{}, map[duplicateKey]string{}
	for _, s := range scored {
		d := docs[s.order]
		exact, formatted := duplicateKeys(d)
		duplicate := exactSeen[exact]
		if duplicate == "" && formatted.bucket != "" {
			duplicate = formatSeen[formatted]
		}
		if duplicate != "" {
			trace.reject(d, "duplicate_result", duplicate)
			continue
		}
		if limit > 0 && len(kept) >= limit {
			trace.reject(d, "result_limit", "")
			continue
		}
		kept = append(kept, s)
		if exact.bucket != "" {
			exactSeen[exact] = d.ID
		}
		if formatted.bucket != "" {
			formatSeen[formatted] = d.ID
		}
	}
	return kept
}

type duplicateKey struct{ bucket, body string }

func duplicateKeys(d Document) (exact, formatted duplicateKey) {
	if d.Group == "" || strings.TrimSpace(d.Text) == "" {
		return
	}
	title := literalSignature(d.Title)
	if title == "" {
		title = "title:" + EntityNameKey(d.Title)
	} else {
		title = "literal:" + title
	}
	bucket := d.Group + "\x00" + normalizedKind(d.Kind) + "\x00" + title
	body := strings.TrimSpace(strings.ReplaceAll(d.Text, "\r\n", "\n"))
	exact = duplicateKey{bucket, body}
	if !sensitiveClaim(body) {
		formatted = duplicateKey{bucket, literalSignature(body) + "\x00" + proseKey(body)}
	}
	return
}

func sensitiveClaim(text string) bool {
	for _, term := range []string{"不", "未", "无", "禁止", "仅", "只", "必须", "至少", "最多", "not ", "never ", "only ", "must ", "`", "\"", "'", "“", "?", "？"} {
		if strings.Contains(strings.ToLower(text), term) {
			return true
		}
	}
	return false
}
func literalSignature(text string) string {
	values := protectedLiteral.FindAllString(text, -1)
	sort.Strings(values)
	return strings.Join(values, "\x00")
}
func proseKey(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("，,。.；;：:", r) {
			return -1
		}
		return r
	}, text)
}
