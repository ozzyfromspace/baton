// Package upgrade moves the sessions baton hosts onto a newer baton once one is installed, so an update
// reaches running sessions without anyone restarting them.
//
// A host never looks for sessions to restart: each one notices on its own that a newer baton is
// installed (Watch), and its controller restarts it at a safe point, resuming the same conversation on
// the new binary in the same process (Exec). Only a compatible release is taken this way (Compatible):
// across a major version, the human may have something to do first, so the session stays put and the
// human is told once.
package upgrade

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ChangelogURL is where a major update says what changed.
const ChangelogURL = "https://github.com/ozzyfromspace/baton/blob/main/CHANGELOG.md"

var releaseRE = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

// Release reports a published release version ("v0.3.1", "v0.4.0-rc.1"), as opposed to a build from a
// checkout ("dev", or `git describe` output such as "v0.3.0-16-g35ada53-dirty").
func Release(v string) bool { return releaseRE.MatchString(v) }

// Compatible reports whether a session running baton `running` may move to `installed` by itself: both
// are releases, installed is newer and not a pre-release, and the leftmost non-zero part of the version
// is the same (the rule of Cargo's and npm's ^ ranges). Before 1.0 the minor version is the major one:
// v0.3.1 → v0.3.2 qualifies, v0.3.2 → v0.4.0 does not, nor does v1.3.0 → v2.0.0.
func Compatible(running, installed string) bool {
	if !Release(running) || !Release(installed) || strings.Contains(installed, "-") || Compare(installed, running) <= 0 {
		return false
	}
	r, i := parts(running), parts(installed)
	for k := range 3 {
		if r[k] != i[k] {
			return false
		}
		if r[k] != 0 {
			return true
		}
	}
	return false
}

// parts is a release's major, minor and patch numbers.
func parts(v string) [3]int {
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	var out [3]int
	for k, s := range strings.SplitN(core, ".", 3) {
		out[k], _ = strconv.Atoi(s)
	}
	return out
}

// Compare orders semantic versions ("0.1.0", "v0.1.0-rc.2"): -1, 0 or 1. A release sorts after its
// pre-releases; pre-release identifiers compare numerically where they are numbers.
func Compare(a, b string) int {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	ac, apre, _ := strings.Cut(a, "-")
	bc, bpre, _ := strings.Cut(b, "-")
	if c := compareDotted(ac, bc); c != 0 {
		return c
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	}
	return compareDotted(apre, bpre)
}

func compareDotted(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil && xn != yn:
			if xn < yn {
				return -1
			}
			return 1
		case (xerr != nil || yerr != nil) && x != y:
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Bin is where the binary of version v lives under baton's root directory (~/.baton/bin/v0.3.1/baton):
// the plugin's launcher downloads each version there, verified against the plugin's checksums.
func Bin(root, v string) string {
	name := "baton"
	if exeSuffix {
		name += ".exe"
	}
	return filepath.Join(root, "bin", v, name)
}

// Installed returns the newest installed baton: the version the launcher last linked as
// ~/.baton/bin/baton, if its binary is there.
func Installed(root string) (string, bool) {
	link, err := os.Readlink(filepath.Join(root, "bin", "baton"))
	if err != nil {
		return "", false
	}
	v := filepath.Base(filepath.Dir(link))
	if !Release(v) {
		return "", false
	}
	if fi, err := os.Stat(Bin(root, v)); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return "", false
	}
	return v, true
}

// Probe runs a baton binary's `version` and returns the version it reports, which proves the binary
// runs on this machine and is the version it claims to be.
func Probe(path string) (string, error) {
	out, err := exec.Command(path, "version").Output()
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimPrefix(strings.TrimSpace(first), "baton "), nil
}

// Watch reports a newer compatible baton for a session running Running, looking at most once per Every
// (the controller asks on every tick).
type Watch struct {
	Root    string
	Running string
	// Skip is a version not to restart on: one a restart already failed to start.
	Skip  string
	Every time.Duration
	Now   func() time.Time

	checked time.Time
	found   string
}

// Newer returns the version to restart on, or "".
func (w *Watch) Newer() string {
	now := w.Now()
	if !w.checked.IsZero() && now.Sub(w.checked) < w.Every {
		return w.found
	}
	w.checked, w.found = now, ""
	if v, ok := Installed(w.Root); ok && v != w.Skip && Compatible(w.Running, v) {
		w.found = v
	}
	return w.found
}
