package testutil

import "time"

type FakeClock struct {
	NowFn func() time.Time
}

func (f FakeClock) Now() time.Time {
	if f.NowFn != nil {
		return f.NowFn()
	}

	return time.Now()
}
