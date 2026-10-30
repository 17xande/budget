package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHashConcurrencyIsBounded pins the global cap on simultaneous argon2 runs:
// without it, a burst of logins from one client holds ten 64 MiB buffers at once
// and the container is OOM-killed.
func TestHashConcurrencyIsBounded(t *testing.T) {
	var inFlight, peak atomic.Int32
	real := deriveKey
	deriveKey = func(password, salt []byte, time_, memory uint32, threads uint8, keyLen uint32) []byte {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return real(password, salt, time_, memory, threads, keyLen)
	}
	t.Cleanup(func() { deriveKey = real })

	hash, err := HashPassword("pw", Params{Memory: 64, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { CheckPassword(hash, "wrong") })
	}
	wg.Wait()

	if got := peak.Load(); got > maxConcurrentHashes || got < 1 {
		t.Errorf("peak concurrent argon2 runs = %d, want 1..%d", got, maxConcurrentHashes)
	}
}
