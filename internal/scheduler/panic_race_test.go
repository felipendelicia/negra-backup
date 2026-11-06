// internal/scheduler/panic_race_test.go
package scheduler_test

import (
	"os"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// P1: cron.New() — exactly how scheduler.go:31 builds it — installs no
// Recover JobWrapper, so a panic inside the scheduled func (e.g. the
// send-on-closed-channel inside Hub.DispatchJob at hub.go:168, reached from
// Scheduler.triggerJob) unwinds the cron goroutine and kills the process.
// Guarded by an env var: it crashes the test binary on purpose.
func TestPanic_CronNewHasNoRecover(t *testing.T) {
	if os.Getenv("NAT_CRON_PANIC") != "1" {
		t.Skip("set NAT_CRON_PANIC=1 to run (crashes the process on purpose)")
	}
	c := cron.New() // identical to internal/scheduler/scheduler.go:31
	if _, err := c.AddFunc("@every 1s", func() {
		t.Log("cron job fired")
		ch := make(chan []byte, 1)
		close(ch)
		select {
		case ch <- nil: // hub.go:168 on a closed AgentConn.send
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	c.Start()
	defer c.Stop()
	time.Sleep(3 * time.Second)
	t.Log("SURVIVED — cron recovered the panic")
}
