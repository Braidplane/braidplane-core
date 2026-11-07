// Command communityschema emits the public projection of an OpenAPI source.
package main

import (
	"fmt"
	"os"

	"github.com/Braidplane/braidplane-core/pkg/publicschema"
	"github.com/getkin/kin-openapi/openapi3"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: communityschema schema.yml")
		os.Exit(2)
	}
	document, err := openapi3.NewLoader().LoadFromFile(os.Args[1])
	if err == nil {
		var projected []byte
		projected, err = publicschema.Project(document)
		if err == nil {
			_, err = os.Stdout.Write(projected)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
