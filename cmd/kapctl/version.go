package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// version is the build version, stamped at build time via
//
//	-ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// (see the Makefile). Plain `go build` leaves it as "dev".
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "print the kapctl version",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println(version)
	},
}

func init() {
	rootCmd.Version = version
	rootCmd.AddCommand(versionCmd)
}
