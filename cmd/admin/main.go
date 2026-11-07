// Command admin runs local database-operator tasks using protected installation configuration.
package main

import (
	"log"
	"os"
)

func main() {
	if err := runAdminCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		log.Fatal(err)
	}
}
