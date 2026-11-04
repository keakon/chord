//go:build unix

package externalclaude

import (
	"testing"
	"time"
)

func TestSessionFinishDoesNotConfuseStreamDrainWithProcessExit(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		name := "direct"
		if supervised {
			name = "supervised"
		}
		t.Run(name, func(t *testing.T) {
			cfg := ProcessConfig{
				Command: writeSupervisedFixture(t, `
i=0
while [ "$i" -lt 80 ]; do
  printf '%s\n' '{"type":"system","subtype":"init","session_id":"sample"}'
  i=$((i + 1))
done
`),
				KillGrace: 100 * time.Millisecond,
			}
			if supervised {
				cfg.Supervisor = &SupervisorConfig{}
			}
			session, err := Start(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				session.Terminate()
				select {
				case <-session.done:
				case <-time.After(10 * time.Second):
					t.Error("session cleanup did not finish")
				}
			})

			exited := session.exited
			if supervised {
				exited = make(chan struct{})
				go func() {
					_ = session.sup.Wait()
					close(exited)
				}()
			}
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Fatal("fixture did not exit")
			}
			// The process is gone, but queued events still block its scanner.
			session.Finish(50 * time.Millisecond)
			select {
			case <-session.done:
			case <-time.After(10 * time.Second):
				t.Fatal("session did not finish draining")
			}
			for range session.Events() {
			}
			if err := session.Wait(); err != nil {
				t.Fatalf("stream drain turned a clean process exit into a failure: %v", err)
			}
			if !session.ProcessEnded() {
				t.Fatal("process group survived the run")
			}
		})
	}
}

func TestSessionFinishTimesOutWhenProcessStillRunning(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		name := "direct"
		if supervised {
			name = "supervised"
		}
		t.Run(name, func(t *testing.T) {
			cfg := ProcessConfig{
				Command:   writeSupervisedFixture(t, "exec sleep 60\n"),
				KillGrace: 100 * time.Millisecond,
			}
			if supervised {
				cfg.Supervisor = &SupervisorConfig{}
			}
			session, err := Start(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				session.Terminate()
				select {
				case <-session.done:
				case <-time.After(10 * time.Second):
					t.Error("session cleanup did not finish")
				}
			})
			session.Finish(50 * time.Millisecond)
			select {
			case <-session.done:
			case <-time.After(10 * time.Second):
				t.Fatal("session did not enforce the process exit deadline")
			}
			if err := session.Wait(); err == nil {
				t.Fatal("process exit timeout ended without an error")
			}
			if !session.ProcessEnded() {
				t.Fatal("process group survived the exit timeout")
			}
		})
	}
}
