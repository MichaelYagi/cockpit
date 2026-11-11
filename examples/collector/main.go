package main

import (
	"os"

	"github.com/MichaelYagi/cockpit/custom"
)

// main is the whole custom build: the normal cockpit command with the
// files collector added.
func main() { os.Exit(custom.Main(files{procRoot: "/proc"})) }
