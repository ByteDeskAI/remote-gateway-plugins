package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCommitAndLevel(t *testing.T) {
	cases := []struct {
		header, body string
		typ          string
		scopes       string
		breaking     bool
		level        int
	}{
		{"feat(demo): add a tool", "", "feat", "demo", false, bumpMinor},
		{"fix(demo): stop crash", "", "fix", "demo", false, bumpPatch},
		{"perf(demo): faster", "", "perf", "demo", false, bumpPatch},
		{"feat(a, b)!: drop old api", "", "feat", "a,b", true, bumpMajor},
		{"fix(demo): rename", "body\n\nBREAKING CHANGE: renamed x", "fix", "demo", true, bumpMajor},
		{"chore(demo): tidy", "", "chore", "demo", false, bumpNone},
		{"docs: readme", "", "docs", "", false, bumpNone},
		{"Update stuff", "", "", "", false, bumpNone},
	}
	for _, c := range cases {
		got := parseCommit("abc", c.header, c.body)
		if got.Type != c.typ || strings.Join(got.scopes, ",") != c.scopes || got.Breaking != c.breaking || got.level() != c.level {
			t.Errorf("%q: got type=%q scopes=%v breaking=%v level=%d", c.header, got.Type, got.scopes, got.Breaking, got.level())
		}
	}
}

func TestBump(t *testing.T) {
	cases := []struct {
		from  string
		level int
		want  string
	}{
		{"0.0.0", bumpMinor, "0.1.0"},
		{"0.0.0", bumpPatch, "0.0.1"},
		{"0.4.2", bumpMajor, "1.0.0"}, // plain SemVer, even before 1.0
		{"1.2.3", bumpMinor, "1.3.0"},
		{"1.2.3", bumpPatch, "1.2.4"},
	}
	for _, c := range cases {
		v, ok := parseVersion(c.from)
		if !ok {
			t.Fatalf("parse %q", c.from)
		}
		if got := v.bump(c.level).String(); got != c.want {
			t.Errorf("%s bump %d = %s, want %s", c.from, c.level, got, c.want)
		}
	}
	for _, bad := range []string{"v1.0.0", "1.0", "01.0.0", "1.0.0-rc.1"} {
		if _, ok := parseVersion(bad); ok {
			t.Errorf("%q should not parse as X.Y.Z", bad)
		}
	}
}

func TestSetManifestVersionKeepsFormatting(t *testing.T) {
	in := "{\n  \"id\": \"demo\",\n  \"serves\": [{\"version\": \"1.0.0\"}],\n  \"version\":   \"0.0.0\",\n  \"x\": 1\n}\n"
	out, err := setManifestVersion([]byte(in), "0.1.0-dev.3")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(in, `"version":   "0.0.0"`, `"version":   "0.1.0-dev.3"`, 1)
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

func TestFoldChangelog(t *testing.T) {
	in := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- hand note\n\n## [0.1.0] - 2026-01-01\n\n- old\n"
	commits := []Commit{
		{SHA: "1111111aaa", Type: "feat", Subject: "add tool"},
		{SHA: "2222222bbb", Type: "fix", Subject: "fix crash"},
		{SHA: "3333333ccc", Type: "feat", Subject: "drop api", Breaking: true},
	}
	got, err := foldChangelog(in, "1.0.0", "2026-10-04", commits)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Changelog\n\n## [Unreleased]\n\n## [1.0.0] - 2026-10-04\n\n### Added\n\n- hand note\n- add tool (1111111)\n\n" +
		"### Changed\n\n- **BREAKING** drop api (3333333)\n\n### Fixed\n\n- fix crash (2222222)\n\n## [0.1.0] - 2026-01-01\n\n- old\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// --- end to end, against a throwaway Git repository -----------------------

type repo struct {
	t    *testing.T
	root string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	// Keep the user's Git config (signing, hooks) out of the test.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	r := &repo{t, t.TempDir()}
	r.git("init", "-q", "-b", "develop")
	r.write("README.md", "hi\n")
	r.commit("chore: initial")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", r.root}, args...)...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (r *repo) write(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
}

func (r *repo) addPlugin(id, ver string) {
	r.write("plugins/"+id+"/plugin.json", "{\n  \"id\": \""+id+"\",\n  \"version\": \""+ver+"\"\n}\n")
	r.write("plugins/"+id+"/VERSION", ver+"\n")
	r.write("plugins/"+id+"/CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n")
	r.write("plugins/"+id+"/scripts/ci/plugin-build-v1.sh", "#!/bin/sh\n")
}

func (r *repo) plans(mode string, build int) map[string]Plan {
	r.t.Helper()
	list, err := planAll(r.root, mode, build)
	if err != nil {
		r.t.Fatal(err)
	}
	m := map[string]Plan{}
	for _, p := range list {
		m[p.ID] = p
	}
	return m
}

func TestPlanApplyFinalize(t *testing.T) {
	r := newRepo(t)
	r.addPlugin("demo", "0.0.0")
	r.addPlugin("other", "0.0.0")
	r.write("plugins/_template/x", "x")
	r.commit("chore(demo,other): scaffold")

	if got := r.plans("release", 0); len(got) != 0 {
		t.Fatalf("chore-only history must not release, got %v", got)
	}

	r.write("plugins/demo/main.go", "package main\n")
	r.commit("feat(demo): add main")
	r.write("docs/notes.md", "n\n")
	r.commit("feat: unrelated docs path")

	got := r.plans("release", 0)
	if len(got) != 1 || got["demo"].Next != "0.1.0" || got["demo"].Current != "0.0.0" || len(got["demo"].Commits) != 1 {
		t.Fatalf("want only demo 0.0.0 -> 0.1.0, got %+v", got)
	}
	if dev := r.plans("dev", 14); dev["demo"].Version != "0.1.0-dev.14" {
		t.Fatalf("dev version = %q", dev["demo"].Version)
	}
	if problems := checkPlugin(r.root, "demo"); len(problems) != 0 {
		t.Fatalf("check: %v", problems)
	}

	if err := apply(r.root, "demo", "0.1.0", "2026-10-04"); err != nil {
		t.Fatal(err)
	}
	if err := finalize(r.root, []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	if msg := strings.TrimSpace(r.git("log", "-1", "--format=%s")); msg != "chore(release): demo v0.1.0" {
		t.Fatalf("release commit = %q", msg)
	}
	if tag := strings.TrimSpace(r.git("tag", "--list", "demo/v*")); tag != "demo/v0.1.0" {
		t.Fatalf("tag = %q", tag)
	}
	if typ := strings.TrimSpace(r.git("cat-file", "-t", "demo/v0.1.0")); typ != "tag" {
		t.Fatalf("tag must be annotated, got %q", typ)
	}
	if problems := checkPlugin(r.root, "demo"); len(problems) != 0 {
		t.Fatalf("check after release: %v", problems)
	}
	cl, _ := os.ReadFile(filepath.Join(r.root, "plugins/demo/CHANGELOG.md"))
	if !strings.Contains(string(cl), "## [Unreleased]\n\n## [0.1.0] - 2026-10-04\n\n### Added\n\n- add main (") {
		t.Fatalf("changelog:\n%s", cl)
	}
	if status := r.git("status", "--porcelain"); status != "" {
		t.Fatalf("finalize left changes: %s", status)
	}

	// Nothing since the tag: no release.
	if got := r.plans("release", 0); len(got) != 0 {
		t.Fatalf("no commits since tag must not release, got %v", got)
	}

	// A breaking change bumps the major version, even before 1.0.
	r.write("plugins/demo/main.go", "package main // v2\n")
	r.commit("feat(demo)!: replace the api")
	if p := r.plans("release", 0)["demo"]; p.Current != "0.1.0" || p.Next != "1.0.0" {
		t.Fatalf("breaking: got %+v", p)
	}
}

func TestPlanUntaggedVersionIsCurrentRelease(t *testing.T) {
	r := newRepo(t)
	r.write("plugins/moved/main.go", "x")
	r.commit("feat(moved): history from before the move")
	r.addPlugin("moved", "0.3.0")
	r.commit("chore(moved): import at 0.3.0")
	if got := r.plans("release", 0); len(got) != 0 {
		t.Fatalf("history before VERSION was set must not count, got %v", got)
	}
	r.write("plugins/moved/main.go", "y")
	r.commit("fix(moved): a fix")
	if p := r.plans("release", 0)["moved"]; p.Current != "0.3.0" || p.Next != "0.3.1" {
		t.Fatalf("got %+v", p)
	}
}

func TestCheckCommits(t *testing.T) {
	r := newRepo(t)
	base := strings.TrimSpace(r.git("rev-parse", "HEAD"))
	r.write("plugins/a/x", "1")
	r.commit("feat(a): fine")
	r.write("plugins/a/x", "2")
	r.write("plugins/b/x", "2")
	r.commit("feat(a,b): fine too")
	r.write("plugins/b/x", "3")
	r.commit("fix(a): wrong scope")
	r.write("plugins/a/x", "4")
	r.commit("not conventional")
	r.write("tools/x", "1")
	r.commit("ci: no plugin scope needed")
	r.write("plugins/_template/x", "1")
	r.commit("chore: template needs no scope")

	problems, err := checkCommits(r.root, base+"..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 || !strings.Contains(problems[0]+problems[1], "wrong scope") || !strings.Contains(problems[0]+problems[1], "not a conventional commit") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestCheckPluginProblems(t *testing.T) {
	r := newRepo(t)
	r.addPlugin("Bad_ID", "1.0")
	r.write("plugins/Bad_ID/CHANGELOG.md", "# Changelog\n")
	if err := os.Chmod(filepath.Join(r.root, "plugins/Bad_ID/scripts/ci/plugin-build-v1.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(checkPlugin(r.root, "Bad_ID"), "\n")
	for _, want := range []string{"kebab-case", "VERSION must be", "Unreleased", "not executable"} {
		if !strings.Contains(problems, want) {
			t.Errorf("missing %q in:\n%s", want, problems)
		}
	}
}
