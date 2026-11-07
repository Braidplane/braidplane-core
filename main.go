package main

import (
	"log"
	"os"

	"github.com/Braidplane/braidplane-core/backend"
)

func main() {
	if err := backend.RunCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		log.Fatal(err)
	}
}
