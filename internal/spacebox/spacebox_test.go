package spacebox

import "testing"

// TestIsInstalledDoesNotPanic exercises the PATH lookup. The boolean
// return depends on the host machine; we only assert the call is safe.
func TestIsInstalledDoesNotPanic(_ *testing.T) {
	_ = IsInstalled()
}
