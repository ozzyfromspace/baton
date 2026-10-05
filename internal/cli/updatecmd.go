package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/version"
)

func init() {
	register("update", "update baton to the latest release (the plugin, then its binary): update [--check]", cmdUpdate)
}

// releasesURL is where the latest release is looked up (BATON_RELEASES_URL overrides it, for tests).
const releasesURL = "https://api.github.com/repos/ozzyfromspace/baton/releases/latest"

// cmdUpdate brings baton up to the latest release. The binary's version follows the plugin's (the
// launcher downloads the binary the installed plugin pins, and verifies it against the plugin's
// checksums), so updating is: let Claude Code update the plugin, then run the new plugin's launcher once
// so the matching binary is downloaded, verified and linked as `baton` before anything needs it.
func cmdUpdate(args []string, io IO) int {
	p, err := parseArgs(args, nil, []string{"check"})
	if err != nil || len(p.pos) != 0 {
		return fail(io, "usage: baton update [--check]%s", errSuffix(err))
	}
	claude := io.Env("BATON_CLAUDE")
	if claude == "" {
		claude = "claude"
	}
	latest, err := latestRelease(io)
	if err != nil {
		return fail(io, "cannot find the latest release: %v", err)
	}
	plug, err := installedPlugin(claude)
	if err != nil {
		return fail(io, "%v", err)
	}
	fmt.Fprintf(io.Out, "baton: latest release %s; installed plugin %s; this binary %s\n", latest, "v"+plug.Version, version.Version)
	pluginCurrent := compareVersions(plug.Version, latest) >= 0
	binaryCurrent := devBuild(version.Version) || compareVersions(version.Version, latest) >= 0
	if pluginCurrent && binaryCurrent {
		fmt.Fprintln(io.Out, "baton: up to date.")
		return 0
	}
	if p.bools["check"] {
		fmt.Fprintf(io.Out, "baton: %s is available; run `baton update` (or /baton update) to install it.\n", latest)
		return 0
	}

	if !pluginCurrent {
		// The marketplace is refreshed first: the plugin update reads its catalog.
		for _, step := range [][]string{{"plugin", "marketplace", "update", marketplaceOf(plug.ID)}, {"plugin", "update", plug.ID, "--scope", plug.Scope}} {
			fmt.Fprintf(io.Out, "baton: running claude %s\n", strings.Join(step, " "))
			if out, err := run(claude, step, nil, 3*time.Minute); err != nil {
				return fail(io, "claude %s failed: %v\n%s", strings.Join(step, " "), err, out)
			}
		}
		if plug, err = installedPlugin(claude); err != nil {
			return fail(io, "%v", err)
		}
		if compareVersions(plug.Version, latest) < 0 {
			return fail(io, "the plugin is still at v%s after the update; try `claude plugin marketplace update` and `claude plugin update %s` yourself", plug.Version, plug.ID)
		}
	}

	// Fetch the binary the new plugin pins, through its own launcher (which verifies the checksum).
	// BATON_BIN is dropped so the launcher does not hand over to the binary that is running now.
	launcher := filepath.Join(plug.InstallPath, "bin", "baton")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BATON_BIN=") {
			env = append(env, kv)
		}
	}
	out, err := run(launcher, []string{"version"}, env, 3*time.Minute)
	if err != nil {
		return fail(io, "the plugin updated to v%s, but its binary could not be installed: %v\n%s", plug.Version, err, out)
	}
	fmt.Fprint(io.Out, out)
	fmt.Fprintf(io.Out, "baton: updated to v%s. Sessions already running keep the old version until they restart: exit and run `baton --continue` (or start a new one).\n", plug.Version)
	return 0
}

// devBuild reports a binary built from a checkout rather than a release (`make build` stamps it with
// `git describe`, e.g. v0.1.0-rc.2-3-gcec9574-dirty): there is nothing to update it to.
func devBuild(v string) bool { return v == "dev" || strings.Contains(v, "-g") }

// plugin is one entry of `claude plugin list --json`.
type plugin struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
}

// installedPlugin finds the baton plugin among the plugins Claude Code has installed.
func installedPlugin(claude string) (plugin, error) {
	out, err := run(claude, []string{"plugin", "list", "--json"}, nil, time.Minute)
	if err != nil {
		return plugin{}, fmt.Errorf("cannot list Claude Code's plugins (%v); is `claude` on your PATH?", err)
	}
	var list []plugin
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		var wrapped struct{ Plugins []plugin }
		if json.Unmarshal([]byte(out), &wrapped) != nil {
			return plugin{}, fmt.Errorf("cannot read `claude plugin list --json`: %v", err)
		}
		list = wrapped.Plugins
	}
	for _, p := range list {
		if strings.HasPrefix(p.ID, "baton@") {
			if p.Scope == "" {
				p.Scope = "user"
			}
			return p, nil
		}
	}
	return plugin{}, fmt.Errorf("the baton plugin is not installed through a marketplace here (a development checkout?); install it with `claude plugin marketplace add ozzyfromspace/baton` and `claude plugin install baton@baton`")
}

func marketplaceOf(id string) string {
	if _, m, ok := strings.Cut(id, "@"); ok {
		return m
	}
	return "baton"
}

// latestRelease asks GitHub for the newest published (non-pre-) release.
func latestRelease(io IO) (string, error) {
	url := io.Env("BATON_RELEASES_URL")
	if url == "" {
		url = releasesURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", url, resp.Status)
	}
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil || rel.Tag == "" {
		return "", fmt.Errorf("unexpected answer from %s", url)
	}
	return rel.Tag, nil
}

// run runs a command and returns its combined output.
func run(name string, args, env []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// compareVersions orders semantic versions ("0.1.0", "v0.1.0-rc.2"): -1, 0 or 1. A release sorts after
// its pre-releases; pre-release identifiers compare numerically where they are numbers.
func compareVersions(a, b string) int {
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
