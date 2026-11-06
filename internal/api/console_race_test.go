// internal/api/console_race_test.go
package api

import (
	"fmt"
	"sync"
	"testing"
)

// C0: handleConsoleWS subscribes (console.go:135) BEFORE snapshotting
// (console.go:140). A line written in between is delivered twice.
func TestConsole_SubscribeBeforeSnapshotDuplicatesLines(t *testing.T) {
	h := NewConsoleHub()
	h.Write([]byte("old line\n"))

	ch := h.subscribe() // console.go:135
	h.Write([]byte("racing line\n"))
	snap := h.snapshot() // console.go:140

	inSnapshot := false
	for _, l := range snap {
		if string(l) == "racing line\n" {
			inSnapshot = true
		}
	}
	inChannel := false
	select {
	case l := <-ch:
		inChannel = string(l) == "racing line\n"
	default:
	}
	if inSnapshot && inChannel {
		t.Log("CONFIRMED: the same line is replayed from history AND pushed live -> duplicate in the Console UI")
	} else {
		t.Errorf("no duplication: snapshot=%v channel=%v", inSnapshot, inChannel)
	}
	h.unsubscribe(ch)
}

// C1: subscribe/unsubscribe/Write in parallel.
func TestRace_ConsoleHubSubscribeWrite(t *testing.T) {
	h := NewConsoleHub()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				ch := h.subscribe()
				h.unsubscribe(ch)
			}
		}()
	}
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				h.Write([]byte(fmt.Sprintf("line %d/%d\n", g, i)))
			}
		}(g)
	}
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				_ = h.snapshot()
			}
		}()
	}
	wg.Wait()
}
