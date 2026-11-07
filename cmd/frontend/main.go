package main

import (
	"log"

	"github.com/Braidplane/braidplane-core/frontend"
	"github.com/Braidplane/braidplane-core/frontend/server"
)

func main() {
	if err := server.Run(func(string) (frontend.RoutePresentation, error) {
		return frontend.CommunityPresentation{}, nil
	}); err != nil {
		log.Fatal(err)
	}
}
