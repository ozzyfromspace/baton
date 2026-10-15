package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// A host records its process where its instance is linked, as hosts/<instance>.pid, before it starts
// claude, and drops the record once claude has exited. A session counts as hosted only while that process
// is one of its ancestors (package hostcheck): a conversation Claude Code moves to its background service
// inherits every BATON_* variable of the hosted claude that started the service, with no host above it.

// pidSuffix names a host's record of its process in the hosts directory, beside its link.
const pidSuffix = ".pid"

// SetHostPID records pid as the process of the host instance.
func (p *Project) SetHostPID(instance string, pid int) error {
	if !validID(instance) || pid <= 0 {
		return fmt.Errorf("cannot record pid %d for host %q", pid, instance)
	}
	return p.writeLink(strconv.Itoa(pid), hostsDir, instance+pidSuffix)
}

// DropHostPID removes the record of the host instance's process: its claude has exited.
func (p *Project) DropHostPID(instance string) {
	if validID(instance) {
		os.Remove(p.path(hostsDir, instance+pidSuffix))
	}
}

// HostPID reads the process recorded for the host instance in the project whose .baton directory is dir,
// or 0 for none. It opens no project and takes no lock: every hook and command of a session asks it.
func HostPID(dir, instance string) int {
	if dir == "" || !validID(instance) {
		return 0
	}
	pid, err := strconv.Atoi(readLine(filepath.Join(dir, hostsDir, instance+pidSuffix)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// inheritedDir holds a mark for each session baton told that it only inherited baton's environment.
const inheritedDir = "inherited"

// TellInherited reports whether session, one that only inherited baton's environment (package hostcheck),
// is yet to be told so, and marks it told: true once per session. dir is the project's .baton directory, as
// the session inherited it. It fails when no mark can be kept there.
func TellInherited(dir, session string) (bool, error) {
	if dir == "" || !validID(session) {
		return false, fmt.Errorf("no mark for session %q in %q", session, dir)
	}
	if _, err := os.Stat(dir); err != nil {
		return false, err
	}
	path := filepath.Join(dir, inheritedDir, session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, f.Close()
}
