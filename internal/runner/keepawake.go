package runner

import (
	"os/exec"
	"runtime"
	"strconv"
)

// KeepAwakeAvailable reports whether this machine can keep itself awake during
// builds: macOS with caffeinate on the PATH.
func KeepAwakeAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("caffeinate")
	return err == nil
}

// keepAwake starts `caffeinate -i -w pid`, which holds an idle-sleep assertion
// until the build process exits. It is a sibling process, so the build's argv,
// process group and cancel signals stay as they are. Failure only costs the
// assertion, never the build.
func (r *Runner) keepAwake(rn *run, pid int) {
	if r.KeepAwake == nil || !r.KeepAwake() || !KeepAwakeAvailable() {
		return
	}
	c := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(pid))
	if err := c.Start(); err != nil {
		rn.writeLine("[buildyard] keep awake: " + err.Error())
		return
	}
	rn.mu.Lock()
	rn.keptAwake = true
	rn.mu.Unlock()
	rn.writeLine("[buildyard] keeping the Mac awake: caffeinate -i -w " + strconv.Itoa(pid))
	r.log.Info("keeping the Mac awake", "job", rn.job.ID, "build pid", pid, "caffeinate pid", c.Process.Pid)
	go c.Wait() // exits on its own when pid does
}

// KeptAwake reports whether caffeinate is keeping the Mac awake for an active job.
func (r *Runner) KeptAwake(id int64) bool {
	r.mu.Lock()
	rn, ok := r.runs[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return rn.keptAwake
}
