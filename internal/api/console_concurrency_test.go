// internal/api/console_concurrency_test.go
package api

import (
	"sync"
	"testing"
)

// ConsoleHub.Write is the io.Writer behind the global logger, so this fan-out runs
// on every log line in the process — including from goroutines with no recover.
// Racing it against unsubscribe used to panic with "send on closed channel".
// Run with -race.
func TestConsoleHub_WriteRacesUnsubscribe(t *testing.T) {
	hub := NewConsoleHub()

	const rounds = 500
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			ch := hub.subscribe()
			go func() {
				for range ch {
				}
			}()
			hub.unsubscribe(ch)
		}
	}()

	wg.Add(4)
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				_, _ = hub.Write([]byte("a log line\n"))
			}
		}()
	}

	wg.Wait()
}

// snapshot reads the ring buffer while Write mutates it.
func TestConsoleHub_SnapshotRacesWrite(t *testing.T) {
	hub := NewConsoleHub()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_, _ = hub.Write([]byte("line\n"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = hub.snapshot()
		}
	}()

	wg.Wait()
}
