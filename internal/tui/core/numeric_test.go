package core

import "testing"

func TestNumericJump(t *testing.T) {
	cases := []struct {
		name     string
		buf      string
		digit    string
		listSize int
		wantBuf  string
		wantIdx  int
		wantHit  bool
	}{
		{"small list commits immediately", "", "3", 5, "", 3, true},
		{"small list out of range", "", "7", 5, "", 0, true},
		{"large list waits for second digit", "", "1", 25, "1", 0, false},
		{"large list two digits commit", "1", "2", 25, "", 12, true},
		{"large list high first digit commits", "", "9", 25, "", 9, true},
		{"unambiguous prefix commits early", "", "3", 25, "", 3, true},
		{"two digits out of range", "9", "9", 25, "", 0, true},
		{"zero never selects", "", "0", 5, "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf, idx, hit := NumericJump(c.buf, c.digit, c.listSize)
			if buf != c.wantBuf || idx != c.wantIdx || hit != c.wantHit {
				t.Errorf("NumericJump(%q, %q, %d) = (%q, %d, %v), want (%q, %d, %v)",
					c.buf, c.digit, c.listSize, buf, idx, hit, c.wantBuf, c.wantIdx, c.wantHit)
			}
		})
	}
}
