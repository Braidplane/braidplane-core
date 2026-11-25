package frontend

import (
	_ "embed"
	"encoding/csv"
	"strings"

	"github.com/gofiber/fiber/v3"
)

//go:embed authorization_matrix.csv
var uiAuthorizationMatrix string

// Check only the Community routes registered in this call. Hosts can mount
// other apps and test fixtures on the same Fiber app.
func checkCommunityRoutes(app *fiber.App) func() {
	rows, err := csv.NewReader(strings.NewReader(uiAuthorizationMatrix)).ReadAll()
	if err != nil {
		panic(err)
	}
	mapped := make(map[string]bool, len(rows))
	for _, row := range rows[1:] {
		mapped[row[0]+" "+row[1]] = true
	}
	for _, route := range app.GetRoutes(true) {
		mapped[route.Method+" "+route.Path] = true
	}
	return func() {
		for _, route := range app.GetRoutes(true) {
			method := route.Method
			if method == fiber.MethodHead {
				method = fiber.MethodGet
			}
			if !mapped[method+" "+route.Path] && !mapped["ANY "+route.Path] {
				panic("missing authorization classification for " + method + " " + route.Path)
			}
		}
	}
}
