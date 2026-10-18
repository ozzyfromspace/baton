package risk

import (
	"path"
	"strings"
)

// What kind of file a changed path is, by its name alone: a test, code, a manifest or lockfile, a
// migration, or something else (docs, data, configuration). The tables are deliberately plain: a file a
// project names oddly is scored as something else, which adds less, never more.

// containers are the top-level directories whose next level is the area a path belongs to
// (internal/loop, src/api, packages/web).
var containers = map[string]bool{
	"internal": true, "src": true, "pkg": true, "packages": true, "apps": true, "app": true, "lib": true, "libs": true,
	"cmd": true, "services": true, "modules": true, "crates": true, "plugins": true,
}

// Area is the part of the project p belongs to: its top-level directory, or its first two levels under a
// container (internal/loop); "." for a file at the root.
func Area(p string) string {
	parts := strings.Split(p, "/")
	switch {
	case len(parts) == 1:
		return "."
	case containers[parts[0]] && len(parts) > 2:
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// codeExts are the extensions of source files.
var codeExts = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".py": true, ".rs": true,
	".rb": true, ".java": true, ".kt": true, ".kts": true, ".swift": true, ".c": true, ".cc": true, ".cpp": true, ".h": true,
	".hpp": true, ".cs": true, ".php": true, ".scala": true, ".vue": true, ".svelte": true, ".ex": true, ".exs": true,
	".erl": true, ".dart": true, ".lua": true, ".sh": true, ".ps1": true, ".sql": true, ".m": true, ".mm": true, ".zig": true,
}

// testDirs are directories whose files are tests.
var testDirs = map[string]bool{"test": true, "tests": true, "__tests__": true, "spec": true, "specs": true, "e2e": true, "testdata": true}

// IsTest reports a test file: named as one (x_test.go, test_x.py, x.test.ts, x.spec.js, XTest.java), or
// under a test directory (test/, tests/, __tests__/, spec/, e2e/, testdata/).
func IsTest(p string) bool {
	base := path.Base(p)
	stem := strings.TrimSuffix(base, path.Ext(base))
	switch {
	case strings.HasSuffix(stem, "_test"), strings.HasPrefix(base, "test_"), strings.HasSuffix(stem, ".test"),
		strings.HasSuffix(stem, ".spec"), strings.HasSuffix(stem, "_spec"), strings.HasSuffix(stem, "Test") && codeExts[path.Ext(base)],
		strings.HasSuffix(stem, "Tests") && codeExts[path.Ext(base)]:
		return true
	}
	for _, dir := range strings.Split(path.Dir(p), "/") {
		if testDirs[dir] {
			return true
		}
	}
	return false
}

// IsCode reports a source file that is not a test.
func IsCode(p string) bool {
	return codeExts[strings.ToLower(path.Ext(p))] && !IsTest(p) && !IsMigration(p) && !Generated(p)
}

// manifests are the files that say what a project depends on, and lockfiles what it resolved to.
var manifests = map[string]bool{
	"go.mod": true, "go.sum": true, "go.work": true, "package.json": true, "package-lock.json": true, "pnpm-lock.yaml": true,
	"pnpm-workspace.yaml": true, "yarn.lock": true, "bun.lockb": true, "bun.lock": true, "npm-shrinkwrap.json": true,
	"Cargo.toml": true, "Cargo.lock": true, "pyproject.toml": true, "poetry.lock": true, "uv.lock": true, "Pipfile": true,
	"Pipfile.lock": true, "setup.py": true, "setup.cfg": true, "Gemfile": true, "Gemfile.lock": true, "composer.json": true,
	"composer.lock": true, "build.gradle": true, "build.gradle.kts": true, "settings.gradle": true, "pom.xml": true,
	"Package.swift": true, "Package.resolved": true, "mix.exs": true, "mix.lock": true, "pubspec.yaml": true, "pubspec.lock": true,
	"flake.lock": true, "deno.json": true, "deno.lock": true,
}

// IsManifest reports a dependency manifest or lockfile (go.mod, package.json, pnpm-lock.yaml, Cargo.lock,
// requirements.txt, a .csproj).
func IsManifest(p string) bool {
	base := path.Base(p)
	return manifests[base] || strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt") || strings.HasSuffix(base, ".csproj")
}

// IsLockfile reports a lockfile: what a manifest resolved to, which tools rewrite by the thousand lines.
func IsLockfile(p string) bool {
	base := path.Base(p)
	return strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".lockb") || base == "go.sum" || base == "package-lock.json" ||
		base == "pnpm-lock.yaml" || base == "npm-shrinkwrap.json" || base == "Package.resolved"
}

// IsMigration reports a database migration: a file under a migrations directory (migrations/, migrate/,
// alembic/versions/, prisma/migrations/).
func IsMigration(p string) bool {
	dirs := strings.Split(path.Dir(p), "/")
	for i, d := range dirs {
		switch d {
		case "migrations", "migration", "migrate":
			return true
		case "versions":
			if i > 0 && dirs[i-1] == "alembic" {
				return true
			}
		}
	}
	return false
}

// Generated reports a file nobody writes by hand: vendored, built or minified, or a snapshot.
func Generated(p string) bool {
	for _, d := range strings.Split(path.Dir(p), "/") {
		switch d {
		case "vendor", "node_modules", "dist", "build", "__snapshots__", ".next", "target":
			return true
		}
	}
	base := path.Base(p)
	return strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") || strings.HasSuffix(base, ".snap") ||
		strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, "_gen.go") || strings.HasSuffix(base, ".generated.ts")
}

// Names reports whether named, a path a plan's section names (plan.Paths: as written, perhaps relative to a
// directory the plan leaves implied, perhaps a directory), is p: the same path, its ending, or a directory
// p is under.
func Names(named, p string) bool {
	named = strings.Trim(named, "/")
	return named != "" && (p == named || strings.HasSuffix(p, "/"+named) || strings.HasPrefix(p, named+"/"))
}
