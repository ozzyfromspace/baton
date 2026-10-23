package lane

import (
	"regexp"
	"slices"
	"strings"
)

// A Preset knows a test runner: the lines of its output that name a failed test (read through a Reading),
// how a run of it is narrowed to some tests through its environment, and the command that reruns only them.
type Preset struct {
	Name string
	// Narrow is the variable, and its value, that narrows a run of the runner to the tests ids; nil where no
	// variable does, and a stage narrowed to ids gives its command only EnvFailed and EnvFailedRE.
	Narrow func(ids []string) (name, value string)
	// Rerun is the command line that runs only the tests fs, all this runner's (FailedTest.Runner); "" for none.
	Rerun func(fs []FailedTest) string
	// Names are the ways a command can name the test f: its id, the name the runner selects it by, its file
	// or package when it failed whole. A command that names a test by any of them runs it.
	Names func(f FailedTest) []string
	// Cmd is the runner's command line before its arguments.
	Cmd string
	// Args are the runner's own arguments that run only the tests ids (none of them failed whole), for a
	// stage whose command runs the runner itself and passes them on (EnvFailedArgs): the one way to narrow a
	// runner no variable narrows. Words are joined by spaces and none holds one, so that an unquoted
	// $BATON_FAILED_ARGS passes them; "" where they cannot be.
	Args func(ids []string) string

	read func(r *Reading) reader // a fresh reader of the runner's output, adding to r
}

// The runners baton knows, by Preset.Name.
const (
	RunGo         = "go"
	RunPytest     = "pytest"
	RunVitest     = "vitest"
	RunJest       = "jest"
	RunPlaywright = "playwright"
)

// Presets are the runners baton knows, in the order a message lists them.
var Presets = []Preset{
	{Name: RunGo, Narrow: goNarrow, Rerun: goRerun, Names: goNames, Cmd: "go test", Args: goArgs, read: readGo},
	{Name: RunPytest, Narrow: pytestNarrow, Rerun: pytestRerun, Names: pytestNames, Cmd: "pytest", Args: plainWords, read: lineReader(RunPytest, pytestFailure)},
	{Name: RunVitest, Rerun: jsRerun("vitest run"), Names: jsNames, Cmd: "vitest run", Args: jsArgs, read: readVitest},
	{Name: RunJest, Rerun: jsRerun("jest"), Names: jsNames, Cmd: "jest", Args: jsArgs, read: readJest},
	{Name: RunPlaywright, Rerun: playwrightRerun, Names: playwrightNames, Cmd: "playwright test", Args: plainWords, read: readPlaywright},
}

// PresetFor is the preset of the runner name, reporting whether baton knows it.
func PresetFor(name string) (Preset, bool) {
	i := slices.IndexFunc(Presets, func(p Preset) bool { return p.Name == name })
	if i < 0 {
		return Preset{}, false
	}
	return Presets[i], true
}

// A reader reads a runner's output a line at a time (each without its line end or colour), adding the tests
// it says failed to its Reading; flush adds what it holds back when the output ends.
type reader struct {
	line  func(line string)
	flush func()
}

// lineReader reads a runner whose every failure line names its test whole.
func lineReader(runner string, failure func(line string) string) func(r *Reading) reader {
	return func(r *Reading) reader {
		return reader{line: func(line string) {
			if id := failure(line); id != "" {
				r.add(FailedTest{Runner: runner, ID: id})
			}
		}}
	}
}

// readGo reads go test: `--- FAIL: TestX/sub` names its top-level test, whose package is the one the next
// `FAIL<tab>pkg` line names; a package's `ok` line drops the failure lines before it, which its tests
// printed of a run of their own. A package that fails with no test failed (it did not build, a test timed
// out, TestMain exited) fails whole.
func readGo(r *Reading) reader {
	type held struct {
		id  string
		seq int
	}
	var pending []held
	return reader{
		line: func(line string) {
			if id := goFailure(line); id != "" {
				if !slices.ContainsFunc(pending, func(h held) bool { return h.id == id }) {
					pending = append(pending, held{id, r.next()})
				}
				return
			}
			if pkg, ok := goPackageLine(line, "FAIL"); ok {
				if len(pending) == 0 {
					r.add(FailedTest{Runner: RunGo, ID: pkg, Scope: pkg, Whole: true})
				}
				for _, h := range pending {
					r.addAt(h.seq, FailedTest{Runner: RunGo, ID: h.id, Scope: pkg})
				}
				pending = nil
				return
			}
			if _, ok := goPackageLine(line, "ok"); ok {
				pending = nil
			}
		},
		flush: func() {
			for _, h := range pending {
				r.addAt(h.seq, FailedTest{Runner: RunGo, ID: h.id})
			}
			pending = nil
		},
	}
}

// goFailure reads go test's `--- FAIL: TestX/sub (0.01s)` as its top-level test, TestX: go test -run splits
// its pattern at slashes, and a top-level test runs its subtests.
func goFailure(line string) string {
	rest, ok := strings.CutPrefix(strings.TrimLeft(line, " \t"), "--- FAIL: ")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, " ")
	name, _, _ = strings.Cut(name, "/")
	if !ValidFailID(name) {
		return ""
	}
	return name
}

// goPackageLine reads go test's line for a package that ended, `FAIL<tab>pkg<tab>0.4s`, `FAIL<tab>pkg [build
// failed]` or `ok  <tab>pkg<tab>(cached)`, as its package when it starts with word.
func goPackageLine(line, word string) (string, bool) {
	rest, ok := strings.CutPrefix(line, word)
	if !ok {
		return "", false
	}
	rest, ok = strings.CutPrefix(strings.TrimLeft(rest, " "), "\t")
	if !ok {
		return "", false
	}
	pkg, _, _ := strings.Cut(rest, "\t")
	pkg, _, _ = strings.Cut(pkg, " ")
	return pkg, pkg != "" && ValidFailID(pkg)
}

// goNarrow runs only the top-level tests ids, through GOFLAGS, which go test reads and go build and go vet
// pass over.
func goNarrow(ids []string) (string, string) { return "GOFLAGS", "-run=" + FailedRE(ids) }

// goRerun runs the tests by name in their packages (all of them, when one's package is not known), and a
// package that failed whole on its own.
func goRerun(fs []FailedTest) string {
	var names, pkgs, whole []string
	anywhere := false
	for _, f := range fs {
		switch {
		case f.Whole:
			whole = appendNew(whole, f.ID)
		default:
			names = appendNew(names, f.ID)
			if f.Scope == "" {
				anywhere = true
			} else {
				pkgs = appendNew(pkgs, f.Scope)
			}
		}
	}
	var cmds []string
	if len(names) > 0 {
		if anywhere {
			pkgs = []string{"./..."}
		}
		cmds = append(cmds, "go test -run "+shellQuote(FailedRE(names))+" "+shellWords(pkgs))
	}
	if len(whole) > 0 {
		cmds = append(cmds, "go test "+shellWords(whole))
	}
	return strings.Join(cmds, " && ")
}

// goNames: a test by its name; a package that failed whole by its import path, or the end of it a command
// run in its module gives (internal/lane for example.com/m/internal/lane).
func goNames(f FailedTest) []string {
	if !f.Whole {
		return []string{f.ID}
	}
	names := []string{f.ID}
	for p := f.ID; ; {
		_, rest, ok := strings.Cut(p, "/")
		if !ok || !strings.Contains(rest, "/") {
			return names
		}
		names, p = append(names, rest), rest
	}
}

// pytestFailure reads a pytest line that names a failed test as the test's node id:
//
//	FAILED tests/test_x.py::test_a[p] - AssertionError    the summary (its id ends where its message starts:
//	                                                     at the first " - " past its parameters, if any)
//	tests/test_x.py::test_a[p] FAILED          [ 50%]     -v
//	[gw3] [ 50%] FAILED tests/test_x.py::test_a[p]        -v with pytest-xdist
func pytestFailure(line string) string {
	if rest, ok := strings.CutPrefix(line, "["); ok && strings.HasPrefix(rest, "gw") {
		for strings.HasPrefix(line, "[") { // xdist's worker, then the progress
			end := strings.Index(line, "] ")
			if end < 0 {
				return ""
			}
			line = line[end+2:]
		}
	}
	rest, ok := strings.CutPrefix(line, "FAILED ")
	if !ok {
		return pytestVerbose(line)
	}
	from := 0
	if open := strings.IndexByte(rest, '['); open >= 0 && !strings.Contains(rest[:open], " - ") {
		if end := strings.IndexByte(rest[open:], ']'); end >= 0 {
			from = open + end
		}
	}
	id := rest
	if i := strings.Index(rest[from:], " - "); i >= 0 {
		id = rest[:from+i]
	}
	if id = strings.TrimSpace(id); !strings.Contains(id, "::") || !ValidFailID(id) {
		return ""
	}
	return id
}

// pytestVerbose reads pytest -v's `tests/test_x.py::test_a[p] FAILED [ 50%]`: a node id at the start of the
// line, its path with no space in it.
func pytestVerbose(line string) string {
	at := strings.LastIndex(line, " FAILED")
	if at <= 0 || line[0] == ' ' {
		return ""
	}
	if after := strings.TrimSpace(line[at+len(" FAILED"):]); after != "" && !(strings.HasPrefix(after, "[") && strings.HasSuffix(after, "%]")) {
		return ""
	}
	id := line[:at]
	path, _, ok := strings.Cut(id, "::")
	if !ok || strings.ContainsAny(path, " \t") || !ValidFailID(id) {
		return ""
	}
	return id
}

// pytestNarrow runs only the tests ids, by their names (-k matches names, not paths) through
// PYTEST_ADDOPTS: a test of the same name in another file runs too.
func pytestNarrow(ids []string) (string, string) {
	var names []string
	for _, id := range ids {
		names = appendNew(names, pytestName(id))
	}
	return "PYTEST_ADDOPTS", `-k "` + strings.Join(names, " or ") + `"`
}

// pytestName is the name -k selects the test id by: its last part, without its parameters.
func pytestName(id string) string {
	name := id[strings.LastIndex(id, "::")+2:]
	if i := strings.IndexByte(name, '['); i > 0 {
		name = name[:i]
	}
	return name
}

func pytestRerun(fs []FailedTest) string {
	if len(fs) == 0 {
		return ""
	}
	var ids []string
	for _, f := range fs {
		ids = appendNew(ids, f.ID)
	}
	return "pytest " + shellWords(ids)
}

func pytestNames(f FailedTest) []string {
	names := []string{f.ID}
	if base, _, ok := strings.Cut(f.ID, "["); ok {
		names = append(names, base)
	}
	return appendNew(names, pytestName(f.ID))
}

// The vitest and jest id of a test is `file > suite > name`; of a file that failed whole (it did not load),
// the file.
const jsSep = " > "

// readVitest reads vitest's summary line ` FAIL  file > suite > name` (` FAIL  file [ file ]` for a file that
// did not load), and, for a gate stopped before its summary, a failed file's ` ❯ file (3 tests | 1 failed)`
// and its tests' `× suite > name 5ms` under it. A project's `|name|` before the file is passed over.
func readVitest(r *Reading) reader {
	file := ""
	return reader{line: func(line string) {
		t := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(t, "FAIL "); ok {
			rest = strings.TrimSpace(skipProject(strings.TrimSpace(rest)))
			if path, name, ok := strings.Cut(rest, jsSep); ok && jsPath(path) && name != "" {
				r.add(FailedTest{Runner: RunVitest, ID: path + jsSep + name})
			} else if path, ok := strings.CutSuffix(rest, " ]"); ok {
				if path, again, ok := strings.Cut(path, " [ "); ok && again == path && jsPath(path) {
					r.add(FailedTest{Runner: RunVitest, ID: path, Whole: true})
				}
			}
			return
		}
		if rest, ok := strings.CutPrefix(t, "❯ "); ok {
			rest = skipProject(rest)
			if path, _, ok := strings.Cut(rest, " ("); ok && jsPath(path) {
				file = path
			}
			return
		}
		for _, cross := range []string{"× ", "✕ "} {
			if name, ok := strings.CutPrefix(t, cross); ok && file != "" {
				if name = trimTook(name); name != "" {
					r.add(FailedTest{Runner: RunVitest, ID: file + jsSep + name})
				}
				return
			}
		}
	}}
}

// skipProject passes over a vitest project's `|name| ` at the start of s.
func skipProject(s string) string {
	if rest, ok := strings.CutPrefix(s, "|"); ok {
		if _, after, ok := strings.Cut(rest, "| "); ok {
			return after
		}
	}
	return s
}

// trimTook cuts the time a vitest test line ends with ("5ms", "1.2s").
func trimTook(name string) string {
	if i := strings.LastIndexByte(name, ' '); i > 0 && tookRE.MatchString(name[i+1:]) {
		name = name[:i]
	}
	return strings.TrimSpace(name)
}

var tookRE = regexp.MustCompile(`^\d+(\.\d+)?m?s$`)

// readJest reads jest: `FAIL file` starts a test file's results, and each `● suite › name` under it is a
// failed test (`● Test suite failed to run`: the file failed whole); `PASS file` ends them.
func readJest(r *Reading) reader {
	file := ""
	return reader{line: func(line string) {
		t := strings.TrimSpace(line)
		for _, word := range []string{"FAIL ", "PASS "} {
			if rest, ok := strings.CutPrefix(t, word); ok && !strings.Contains(rest, jsSep) {
				file = ""
				if word == "FAIL " {
					for _, f := range strings.Fields(rest) {
						if jsPath(f) {
							file = f
							break
						}
					}
				}
				return
			}
		}
		name, ok := strings.CutPrefix(t, "● ")
		if !ok || file == "" {
			return
		}
		switch name = strings.TrimSpace(name); {
		case name == "Test suite failed to run":
			r.add(FailedTest{Runner: RunJest, ID: file, Whole: true})
		case name == "" || name == "Console" || strings.HasSuffix(name, ":"):
		default:
			r.add(FailedTest{Runner: RunJest, ID: file + jsSep + strings.ReplaceAll(name, " › ", jsSep)})
		}
	}}
}

// jsRerun runs the files of the tests fs, only the tests by their names (-t, a pattern matched within each
// test's full name); a file that failed whole runs whole, on its own.
func jsRerun(cmd string) func(fs []FailedTest) string {
	return func(fs []FailedTest) string {
		var files, names, whole []string
		for _, f := range fs {
			if f.Whole {
				whole = appendNew(whole, f.ID)
				continue
			}
			file, _, _ := strings.Cut(f.ID, jsSep)
			files = appendNew(files, file)
			names = appendNew(names, regexp.QuoteMeta(jsName(f.ID)))
		}
		var cmds []string
		if len(files) > 0 {
			cmds = append(cmds, cmd+" "+shellWords(files)+" -t "+shellQuote(strings.Join(names, "|")))
		}
		if len(whole) > 0 {
			cmds = append(cmds, cmd+" "+shellWords(whole))
		}
		return strings.Join(cmds, " && ")
	}
}

// jsName is a vitest or jest test's own name, the last part of its id.
func jsName(id string) string {
	if i := strings.LastIndex(id, jsSep); i >= 0 {
		return id[i+len(jsSep):]
	}
	return id
}

func jsNames(f FailedTest) []string {
	if f.Whole {
		return []string{f.ID}
	}
	return appendNew([]string{f.ID}, jsName(f.ID))
}

// readPlaywright reads Playwright's failure lines, the list reporter's `✘  3 [project] › file:line:col ›
// title (1.2s)` and the failure details' `1) [project] › file:line:col › title ───`, as the test's
// `file:line`, in its project. A test that failed and then passed on a retry is read as failed: it failed
// once.
//
// It reads the end-of-run summary too, which every reporter prints and which is all a log's tail may hold:
// under its `  2 failed` line, each indented `[project] › file:line:col › title` (or `file:line:col ›
// title`) is a failed test, up to the next count line (`1 flaky`, `3 skipped`, `40 passed (1.3m)`, …) or a
// line that is not indented. The tests under `N flaky` passed on a retry, and those under `N interrupted`
// never ended: neither is read as failed from the summary.
func readPlaywright(r *Reading) reader {
	failed := false // in the summary's list of failed tests
	return reader{line: func(line string) {
		if f, ok := playwrightFailure(line); ok {
			r.add(f)
			return
		}
		t := strings.TrimSpace(line)
		switch {
		case playwrightFailedRE.MatchString(t):
			failed = true
		case !failed:
		case playwrightCountRE.MatchString(t), t != "" && line[0] != ' ' && line[0] != '\t':
			failed = false
		default:
			if f, ok := playwrightTest(playwrightListedRE.FindStringSubmatch(t)); ok {
				r.add(f)
			}
		}
	}}
}

// playwrightHead is how Playwright names a test: its project, if any, and where it is, then its title.
const playwrightHead = `(?:\[([^\]]+)\] › )?(\S+):(\d+):\d+ › `

var (
	playwrightRE       = regexp.MustCompile(`^(?:(?:✘|x)\s+(?:\d+\s+)?|\d+\)\s+)` + playwrightHead)
	playwrightListedRE = regexp.MustCompile(`^` + playwrightHead)
	// The summary's count lines: `2 failed` starts its list of failed tests, any other count ends it.
	playwrightFailedRE = regexp.MustCompile(`^\d+ failed$`)
	playwrightCountRE  = regexp.MustCompile(`^\d+ \S`)
)

func playwrightFailure(line string) (FailedTest, bool) {
	return playwrightTest(playwrightRE.FindStringSubmatch(strings.TrimSpace(line)))
}

// playwrightTest is the test a match of playwrightHead names.
func playwrightTest(m []string) (FailedTest, bool) {
	if m == nil || !jsPath(m[2]) {
		return FailedTest{}, false
	}
	id := m[2] + ":" + m[3]
	return FailedTest{Runner: RunPlaywright, ID: id, Scope: m[1]}, ValidFailID(id)
}

// playwrightRerun runs the tests by their `file:line`, in their projects when every one has one.
func playwrightRerun(fs []FailedTest) string {
	if len(fs) == 0 {
		return ""
	}
	var ids, projects []string
	all := true
	for _, f := range fs {
		ids = appendNew(ids, f.ID)
		if f.Scope == "" {
			all = false
		} else {
			projects = appendNew(projects, f.Scope)
		}
	}
	cmd := "playwright test " + shellWords(ids)
	if all {
		for _, p := range projects {
			cmd += " " + shellQuote("--project="+p)
		}
	}
	return cmd
}

func playwrightNames(f FailedTest) []string { return []string{f.ID} }

// jsPath reports whether s is the path of a JavaScript or TypeScript file.
func jsPath(s string) bool {
	if strings.ContainsAny(s, " \t") {
		return false
	}
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts"} {
		if len(s) > len(ext) && strings.HasSuffix(s, ext) {
			return true
		}
	}
	return false
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// shellWords joins words for a POSIX shell, quoting each that needs it.
func shellWords(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = shellQuote(w)
	}
	return strings.Join(q, " ")
}

// shellQuote quotes s for a POSIX shell when it holds anything but letters, digits and -_./:=@,+%.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@,+%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
