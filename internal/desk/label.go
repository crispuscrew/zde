package desk

import "strings"

// Label turns an application id into a workspace slot: firefox out of
// org.mozilla.firefox, alacritty out of Alacritty.
//
// A workspace is named after what is in it, so the name on the bar says
// something. The reverse-DNS prefix is dropped because nobody reads
// org.mozilla; what is left is lowercased and stripped to what a name can
// hold. An id that leaves nothing usable returns false, and the caller falls
// back to an ordinal rather than inventing a label.
func Label(appID string) (string, bool) {
	s := appID
	// Reverse-DNS ids: the last segment is the part a person would say.
	if i := strings.LastIndex(s, "."); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	s = strings.ToLower(s)

	var b strings.Builder
	lastDash := true // no leading dash
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" || len(out) > partMax {
		return "", false
	}
	// A label that is all digits would be indistinguishable from an ordinal,
	// and the two spaces have to stay apart.
	if isDigits(out) {
		return "", false
	}
	return out, true
}
