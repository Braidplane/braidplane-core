// Package supplierfixture retains the synthetic Supplier Price Desk
// inputs and independent expectations for unit and connector acceptance tests.
package supplierfixture

import "embed"

//go:embed data
var Files embed.FS
