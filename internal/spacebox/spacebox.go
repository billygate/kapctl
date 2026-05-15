// Package spacebox shells out to an external `spacebox` CLI for local
// kind cluster lifecycle (up/down). IsInstalled lets callers degrade
// gracefully (and hide UI surface) when spacebox is missing from PATH.
package spacebox

import (
	"os"
	"os/exec"
)

// Up runs `spacebox cluster up`, streaming output to the parent process.
func Up() error {
	cmd := exec.Command("spacebox", "cluster", "up")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Down runs `spacebox cluster down`, streaming output to the parent process.
func Down() error {
	cmd := exec.Command("spacebox", "cluster", "down")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// IsInstalled reports whether the spacebox CLI is on PATH.
func IsInstalled() bool {
	_, err := exec.LookPath("spacebox")
	return err == nil
}
