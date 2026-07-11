package core

import "strconv"

// NumericJump implements the shared 1–9 / 2-digit list-jump rules:
// digits accumulate into buf, and the jump commits as soon as the typed
// number is unambiguous — immediately for lists shorter than 10, after
// two digits, or once no further digit could still address a row
// (idx*10 > listSize). It returns the new buffer, the committed 1-based
// index (0 when the typed number addressed no row), and whether the
// jump committed; a false commit means "keep collecting digits".
func NumericJump(buf, digit string, listSize int) (newBuf string, index int, committed bool) {
	buf += digit
	idx, _ := strconv.Atoi(buf)

	if listSize >= 10 && len(buf) < 2 && idx*10 <= listSize {
		return buf, 0, false
	}
	if idx > 0 && idx <= listSize {
		return "", idx, true
	}
	return "", 0, true
}
