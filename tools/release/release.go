package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Versions

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	// A prerelease is allowed only where a version is stamped for a dev build.
	semverPrePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)
	headerPattern    = regexp.MustCompile(`^([a-zA-Z]+)(?:\(([^)]*)\))?(!)?: (.+)$`)
)

type version [3]int

func parseVersion(s string) (version, bool) {
	m := semverPattern.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	var v version
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

func (v version) less(o version) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] < o[i]
		}
	}
	return false
}

// Bump levels, in increasing order.
const (
	bumpNone = iota
	bumpPatch
	bumpMinor
	bumpMajor
)

// bump applies plain SemVer. Before 1.0 a breaking change still bumps the
// major version; we do not use the "0.x minor means breaking" convention.
func (v version) bump(level int) version {
	switch level {
	case bumpMajor:
		return version{v[0] + 1, 0, 0}
	case bumpMinor:
		return version{v[0], v[1] + 1, 0}
	case bumpPatch:
		return version{v[0], v[1], v[2] + 1}
	}
	return v
}

// ---------------------------------------------------------------------------
// Conventional commits

type Commit struct {
	SHA      string `json:"sha"`
	Type     string `json:"type"`
	Subject  string `json:"subject"`
	Breaking bool   `json:"breaking"`
	scopes   []string
	ok       bool // header is a conventional commit
}

func parseCommit(sha, header, body string) Commit {
	c := Commit{SHA: sha, Subject: header}
	m := headerPattern.FindStringSubmatch(strings.TrimSpace(header))
	if m == nil {
		return c
	}
	c.ok = true
	c.Type = strings.ToLower(m[1])
	c.Subject = m[4]
	for _, s := range strings.Split(m[2], ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.scopes = append(c.scopes, s)
		}
	}
	c.Breaking = m[3] == "!"
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "BREAKING CHANGE:") || strings.HasPrefix(line, "BREAKING-CHANGE:") {
			c.Breaking = true
		}
	}
	return c
}

func (c Commit) level() int {
	switch {
	case !c.ok:
		return bumpNone
	case c.Breaking:
		return bumpMajor
	case c.Type == "feat":
		return bumpMinor
	case c.Type == "fix" || c.Type == "perf":
		return bumpPatch
	}
	return bumpNone
}

// ---------------------------------------------------------------------------
// Git

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// gitCommits lists non-merge commits in rangeSpec (empty means all of HEAD)
// that touch path, oldest first.
func gitCommits(root, rangeSpec, path string) ([]Commit, error) {
	args := []string{"log", "--no-merges", "--reverse", "--format=%H%x1f%s%x1f%b%x1e"}
	if rangeSpec != "" {
		args = append(args, rangeSpec)
	} else {
		args = append(args, "HEAD")
	}
	args = append(args, "--", path)
	out, err := git(root, args...)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x1f")
		if len(f) < 3 {
			continue
		}
		commits = append(commits, parseCommit(f[0], f[1], f[2]))
	}
	return commits, nil
}

// ---------------------------------------------------------------------------
// Plugins

// pluginIDs lists the plugin folders under plugins/, skipping "_" folders.
func pluginIDs(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "plugins"))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), "_") && !strings.HasPrefix(e.Name(), ".") {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

func pluginDir(id string) string { return "plugins/" + id }

// checkPlugin returns every contract problem with plugins/<id>/.
func checkPlugin(root, id string) []string {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, id+": "+fmt.Sprintf(format, a...)) }
	dir := filepath.Join(root, pluginDir(id))

	if len(id) < 2 || len(id) > 40 || !idPattern.MatchString(id) {
		add("folder name must be lowercase kebab-case, 2-40 characters")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	fileVersion := strings.TrimSpace(string(raw))
	if err != nil {
		add("VERSION is missing")
	} else if _, ok := parseVersion(fileVersion); !ok || strings.Count(string(raw), "\n") > 1 {
		add("VERSION must be one line holding X.Y.Z, found %q", fileVersion)
	}
	var manifest struct {
		ID      *string `json:"id"`
		Version *string `json:"version"`
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "plugin.json")); err != nil {
		add("plugin.json is missing")
	} else if err := json.Unmarshal(raw, &manifest); err != nil {
		add("plugin.json is not valid JSON: %v", err)
	} else {
		if manifest.ID == nil || *manifest.ID != id {
			add("plugin.json id must equal the folder name %q", id)
		}
		if manifest.Version == nil || *manifest.Version != fileVersion {
			add("plugin.json version must equal VERSION (%s)", fileVersion)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md")); err != nil {
		add("CHANGELOG.md is missing")
	} else if !regexp.MustCompile(`(?m)^## \[Unreleased\]\s*$`).Match(raw) {
		add("CHANGELOG.md has no \"## [Unreleased]\" section")
	}
	hook := filepath.Join(dir, "scripts/ci/plugin-build-v1.sh")
	if info, err := os.Lstat(hook); err != nil || !info.Mode().IsRegular() {
		add("scripts/ci/plugin-build-v1.sh is missing or not a regular file")
	} else if info.Mode().Perm()&0o111 == 0 {
		add("scripts/ci/plugin-build-v1.sh is not executable")
	}
	return problems
}

// checkCommits returns a problem for every commit in rangeSpec that touches a
// plugin folder without naming that plugin in its scope.
func checkCommits(root, rangeSpec string) ([]string, error) {
	out, err := git(root, "log", "--no-merges", "--format=%x1e%H%x1f%s", "--name-only", rangeSpec)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, rec := range strings.Split(out, "\x1e") {
		lines := strings.Split(strings.TrimSpace(rec), "\n")
		f := strings.SplitN(lines[0], "\x1f", 2)
		if len(f) < 2 {
			continue
		}
		touched := map[string]bool{}
		for _, file := range lines[1:] {
			parts := strings.Split(file, "/")
			if len(parts) > 2 && parts[0] == "plugins" && !strings.HasPrefix(parts[1], "_") {
				touched[parts[1]] = true
			}
		}
		if len(touched) == 0 {
			continue
		}
		c := parseCommit(f[0], f[1], "")
		short := f[0][:min(7, len(f[0]))]
		if !c.ok {
			problems = append(problems, fmt.Sprintf("%s %q: not a conventional commit, but it changes a plugin", short, f[1]))
			continue
		}
		if c.Type == "chore" && len(c.scopes) == 1 && c.scopes[0] == "release" {
			continue // the release commit itself
		}
		scoped := map[string]bool{}
		for _, s := range c.scopes {
			scoped[s] = true
		}
		var missing []string
		for id := range touched {
			if !scoped[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			problems = append(problems, fmt.Sprintf("%s %q: changes plugins/%s/ but its scope does not name %s",
				short, f[1], strings.Join(missing, ", plugins/"), strings.Join(missing, ", ")))
		}
	}
	return problems, nil
}

// ---------------------------------------------------------------------------
// Plan

type Plan struct {
	ID      string   `json:"id"`
	Dir     string   `json:"dir"`
	Current string   `json:"current"`
	Next    string   `json:"next"`
	Version string   `json:"version"`
	Commits []Commit `json:"commits"`
}

// lastTag returns the highest X.Y.Z among the <id>/vX.Y.Z tags reachable from
// HEAD, if any.
func lastTag(root, id string) (version, bool, error) {
	out, err := git(root, "tag", "--merged", "HEAD", "--list", id+"/v*")
	if err != nil {
		return version{}, false, err
	}
	var best version
	found := false
	for _, tag := range strings.Fields(out) {
		if v, ok := parseVersion(strings.TrimPrefix(tag, id+"/v")); ok && (!found || best.less(v)) {
			best, found = v, true
		}
	}
	return best, found, nil
}

// planPlugin works out the next release of one plugin. It returns nil when no
// commit since the current release is releasable.
//
// The current release is, in order:
//  1. the highest <id>/vX.Y.Z tag reachable from HEAD; commits after that tag count.
//  2. with no tag, a VERSION above 0.0.0 (a plugin that was released before it
//     moved here); only commits after the last commit that changed VERSION count.
//  3. otherwise 0.0.0, and every commit that touches the plugin counts.
func planPlugin(root, id string) (*Plan, error) {
	dir := pluginDir(id)
	current, tagged, err := lastTag(root, id)
	if err != nil {
		return nil, err
	}
	rangeSpec := ""
	if tagged {
		rangeSpec = id + "/v" + current.String() + "..HEAD"
	} else if raw, err := os.ReadFile(filepath.Join(root, dir, "VERSION")); err == nil {
		if v, ok := parseVersion(strings.TrimSpace(string(raw))); ok && v != (version{}) {
			current = v
			last, err := git(root, "log", "-1", "--format=%H", "--", dir+"/VERSION")
			if err != nil {
				return nil, err
			}
			if last = strings.TrimSpace(last); last != "" {
				rangeSpec = last + "..HEAD"
			}
		}
	}
	commits, err := gitCommits(root, rangeSpec, dir+"/")
	if err != nil {
		return nil, err
	}
	level := bumpNone
	releasable := []Commit{}
	for _, c := range commits {
		if l := c.level(); l > bumpNone {
			level = max(level, l)
			releasable = append(releasable, c)
		}
	}
	if level == bumpNone {
		return nil, nil
	}
	next := current.bump(level).String()
	return &Plan{ID: id, Dir: dir, Current: current.String(), Next: next, Version: next, Commits: releasable}, nil
}

func planAll(root, mode string, build int) ([]Plan, error) {
	ids, err := pluginIDs(root)
	if err != nil {
		return nil, err
	}
	plans := []Plan{}
	for _, id := range ids {
		p, err := planPlugin(root, id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			continue
		}
		if mode == "dev" {
			p.Version = fmt.Sprintf("%s-dev.%d", p.Next, build)
		}
		plans = append(plans, *p)
	}
	return plans, nil
}

// ---------------------------------------------------------------------------
// Writing files

// setManifestVersion rewrites only the value of the top-level "version" key,
// so the rest of plugin.json keeps its order and formatting.
func setManifestVersion(raw []byte, v string) ([]byte, error) {
	depth, inString, escaped := 0, false, false
	keyStart := -1
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
				if depth == 1 && string(raw[keyStart:i+1]) == `"version"` {
					// Is this string a key? Skip spaces and expect ':' then a string.
					j := i + 1
					for j < len(raw) && strings.ContainsRune(" \t\r\n", rune(raw[j])) {
						j++
					}
					if j < len(raw) && raw[j] == ':' {
						j++
						for j < len(raw) && strings.ContainsRune(" \t\r\n", rune(raw[j])) {
							j++
						}
						if j < len(raw) && raw[j] == '"' {
							end := bytes.IndexByte(raw[j+1:], '"')
							if end < 0 {
								break
							}
							quoted, _ := json.Marshal(v)
							out := append(append(append([]byte{}, raw[:j]...), quoted...), raw[j+1+end+1:]...)
							return out, nil
						}
					}
				}
			}
			continue
		}
		switch ch {
		case '"':
			inString, keyStart = true, i
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return nil, fmt.Errorf("plugin.json has no top-level \"version\" string")
}

func writeManifestVersion(path, id, v string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	if m.ID != id {
		return fmt.Errorf("%s: id is %q, expected %q", path, m.ID, id)
	}
	out, err := setManifestVersion(raw, v)
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	return os.WriteFile(path, out, 0o644)
}

// foldChangelog moves the Unreleased notes plus entries generated from commits
// into a new "## [v] - date" section, leaving an empty Unreleased section.
func foldChangelog(text, v, date string, commits []Commit) (string, error) {
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "## [Unreleased]" {
			start = i
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("CHANGELOG.md has no \"## [Unreleased]\" section")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}

	// Group the hand-written notes by their "### " heading.
	var order []string
	groups := map[string][]string{}
	heading := ""
	for _, l := range lines[start+1 : end] {
		if strings.HasPrefix(l, "### ") {
			heading = strings.TrimSpace(strings.TrimPrefix(l, "### "))
			if _, seen := groups[heading]; !seen {
				order = append(order, heading)
				groups[heading] = nil
			}
			continue
		}
		if strings.TrimSpace(l) == "" {
			continue
		}
		if _, seen := groups[heading]; !seen {
			order = append(order, heading)
		}
		groups[heading] = append(groups[heading], l)
	}
	for _, c := range commits {
		short := c.SHA[:min(7, len(c.SHA))]
		group, entry := "", "- "+c.Subject+" ("+short+")"
		switch {
		case c.Breaking:
			group, entry = "Changed", "- **BREAKING** "+c.Subject+" ("+short+")"
		case c.Type == "feat":
			group = "Added"
		case c.Type == "fix" || c.Type == "perf":
			group = "Fixed"
		default:
			continue
		}
		if _, seen := groups[group]; !seen {
			order = append(order, group)
		}
		groups[group] = append(groups[group], entry)
	}
	rank := map[string]int{"": 0, "Added": 1, "Changed": 2, "Fixed": 3}
	sort.SliceStable(order, func(i, j int) bool {
		ri, iok := rank[order[i]]
		rj, jok := rank[order[j]]
		if !iok {
			ri = 4
		}
		if !jok {
			rj = 4
		}
		return ri < rj
	})

	section := []string{"## [Unreleased]", "", "## [" + v + "] - " + date, ""}
	for _, h := range order {
		if len(groups[h]) == 0 {
			continue
		}
		if h != "" {
			section = append(section, "### "+h, "")
		}
		section = append(section, groups[h]...)
		section = append(section, "")
	}
	out := append(append(append([]string{}, lines[:start]...), section...), lines[end:]...)
	return strings.Join(out, "\n"), nil
}

// apply writes VERSION, plugin.json and CHANGELOG.md for one release.
func apply(root, id, v, date string) error {
	if _, ok := parseVersion(v); !ok {
		return fmt.Errorf("--version must be X.Y.Z, got %q", v)
	}
	p, err := planPlugin(root, id)
	if err != nil {
		return err
	}
	var commits []Commit
	if p != nil {
		commits = p.Commits
	}
	dir := filepath.Join(root, pluginDir(id))
	if err := writeManifestVersion(filepath.Join(dir, "plugin.json"), id, v); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(v+"\n"), 0o644); err != nil {
		return err
	}
	clPath := filepath.Join(dir, "CHANGELOG.md")
	raw, err := os.ReadFile(clPath)
	if err != nil {
		return err
	}
	text, err := foldChangelog(string(raw), v, date, commits)
	if err != nil {
		return err
	}
	return os.WriteFile(clPath, []byte(text), 0o644)
}

// finalize commits the applied files of every id once, then tags each id.
func finalize(root string, ids []string) error {
	var names, paths []string
	tags := map[string]string{}
	for _, id := range ids {
		raw, err := os.ReadFile(filepath.Join(root, pluginDir(id), "VERSION"))
		if err != nil {
			return err
		}
		v := strings.TrimSpace(string(raw))
		if _, ok := parseVersion(v); !ok {
			return fmt.Errorf("%s: VERSION %q is not X.Y.Z", id, v)
		}
		names = append(names, id+" v"+v)
		tags[id] = id + "/v" + v
		for _, f := range []string{"VERSION", "plugin.json", "CHANGELOG.md"} {
			paths = append(paths, pluginDir(id)+"/"+f)
		}
	}
	if _, err := git(root, append([]string{"add", "--"}, paths...)...); err != nil {
		return err
	}
	msg := "chore(release): " + strings.Join(names, ", ")
	if _, err := git(root, append([]string{"commit", "-m", msg, "--"}, paths...)...); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := git(root, "tag", "-a", tags[id], "-m", strings.Replace(tags[id], "/v", " v", 1)); err != nil {
			return err
		}
	}
	return nil
}

func today() string { return time.Now().UTC().Format("2006-01-02") }
