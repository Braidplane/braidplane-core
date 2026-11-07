package client

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// The driver's environment reporting bypasses the fake provider transport.
	if err := os.Setenv("SNOWFLAKE_DISABLE_PLATFORM_DETECTION", "true"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
