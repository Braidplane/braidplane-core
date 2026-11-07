package main

import (
	"log"
	"os"

	"github.com/Braidplane/braidplane-core/pkg/fake/command"
)

func main() {
	if err := command.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		log.Fatal(err)
	}
}
