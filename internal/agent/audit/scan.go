package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gleicon/fiia/internal/assert"
	"gopkg.in/yaml.v3"
)

// fileModuleArgs maps file-managing ansible modules to the argument keys
// holding the managed path. Short names match after stripping any
// collection prefix (template == ansible.builtin.template).
var fileModuleArgs = map[string][]string{
	"template":    {"dest"},
	"copy":        {"dest"},
	"file":        {"path", "dest"},
	"lineinfile":  {"path", "dest"},
	"blockinfile": {"path", "dest"},
	"assemble":    {"dest"},
	"get_url":     {"dest"},
}

var createsRe = regexp.MustCompile(`(?:^|\s)creates=(\S+)`)

// ScanPlaybooks extracts managed absolute file paths from ansible playbook
// files. It follows pre_tasks/tasks/post_tasks/handlers, block/rescue/always
// nesting, static include/import_tasks file references, play vars_files, and
// expands static loops over {{ item }}. Anything unresolvable statically
// (templated paths without local vars, dynamic includes, unarchive members)
// is reported in warnings, never silently dropped.
func ScanPlaybooks(paths []string) (files []string, warnings []string, err error) {
	assert.True(len(paths) > 0, "paths must not be empty")
	s := &scanner{visited: map[string]bool{}}
	for _, p := range paths {
		if err := s.scanFile(p, map[string]string{}, &files, &warnings); err != nil {
			return nil, nil, err
		}
	}
	return Dedupe(files), Dedupe(warnings), nil
}

type scanner struct {
	visited map[string]bool
}

func (s *scanner) scanFile(path string, vars map[string]string, files, warnings *[]string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", path, err)
	}
	if s.visited[abs] {
		return nil
	}
	s.visited[abs] = true

	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read playbook %q: %w", path, err)
	}
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse playbook %q: %w", path, err)
	}

	dir := filepath.Dir(abs)
	plays, ok := doc.([]any)
	if !ok {
		if single, ok := doc.(map[string]any); ok {
			plays = []any{single}
		} else {
			return fmt.Errorf("parse playbook %q: top level must be a play list", path)
		}
	}
	// Included task files are bare task lists, not plays: scan them directly.
	if len(plays) > 0 {
		if first, ok := plays[0].(map[string]any); ok && !isPlay(first) {
			s.scanTaskList(plays, vars, dir, files, warnings)
			return nil
		}
	}
	for _, item := range plays {
		play, ok := item.(map[string]any)
		if !ok {
			continue
		}
		playVars := mergeVars(vars, playVarsOf(play, dir, warnings))
		for _, section := range []string{"pre_tasks", "tasks", "post_tasks", "handlers"} {
			s.scanTaskList(asList(play[section]), playVars, dir, files, warnings)
		}
	}
	return nil
}

// isPlay reports whether m looks like a play (has hosts or task sections)
// rather than a bare task from an included task file.
func isPlay(m map[string]any) bool {
	if _, ok := m["hosts"]; ok {
		return true
	}
	for _, section := range []string{"tasks", "pre_tasks", "post_tasks", "handlers", "roles"} {
		if _, ok := m[section]; ok {
			return true
		}
	}
	return false
}

// playVarsOf merges a play's vars: mapping plus best-effort vars_files
// (top-level scalar keys only; unparseable files produce a warning).
func playVarsOf(play map[string]any, dir string, warnings *[]string) map[string]string {
	out := map[string]string{}
	if m, ok := play["vars"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := scalarString(v); ok {
				out[k] = s
			}
		}
	}
	for _, f := range asStringList(play["vars_files"]) {
		if !filepath.IsAbs(f) {
			f = filepath.Join(dir, f)
		}
		data, err := os.ReadFile(f)
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("vars_file unreadable, vars inside left unresolved: %s", f))
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			*warnings = append(*warnings, fmt.Sprintf("vars_file unparseable, vars inside left unresolved: %s", f))
			continue
		}
		for k, v := range doc {
			if s, ok := scalarString(v); ok {
				out[k] = s
			}
		}
	}
	return out
}

func (s *scanner) scanTaskList(tasks []any, vars map[string]string, dir string, files, warnings *[]string) {
	for _, item := range tasks {
		task, ok := item.(map[string]any)
		if !ok {
			continue
		}
		localVars := mergeVars(vars, taskVarsOf(task))
		taskName := displayName(task)

		// Nested blocks.
		nested := false
		for _, section := range []string{"block", "rescue", "always"} {
			if sub, ok := task[section].([]any); ok {
				nested = true
				s.scanTaskList(sub, localVars, dir, files, warnings)
			}
		}
		if nested {
			continue
		}

		// Static task-file includes.
		if inc := includeFileOf(task); inc != "" {
			inc = substituteVars(inc, localVars)
			if hasTemplate(inc) {
				*warnings = append(*warnings, fmt.Sprintf("task %q: dynamic include left unresolved: %s", taskName, inc))
				continue
			}
			if !filepath.IsAbs(inc) {
				inc = filepath.Join(dir, inc)
			}
			if err := s.scanFile(inc, localVars, files, warnings); err != nil {
				*warnings = append(*warnings, fmt.Sprintf("task %q: %v", taskName, err))
			}
			continue
		}

		loopItems := loopItemsOf(task)
		for key, val := range task {
			mod := shortModule(key)
			args, ok := fileModuleArgs[mod]
			if !ok {
				continue
			}
			s.scanModuleArgs(mod, args, val, task, taskName, localVars, loopItems, files, warnings)
		}

		// command/shell/script creates= paths.
		s.scanCommandCreates(task, taskName, localVars, loopItems, files, warnings)
	}
}

func (s *scanner) scanModuleArgs(mod string, args []string, val any, task map[string]any, taskName string, vars map[string]string, loop []string, files, warnings *[]string) {
	argMap, ok := val.(map[string]any)
	if !ok {
		*warnings = append(*warnings, fmt.Sprintf("task %q: %s args not a mapping, skipped", taskName, mod))
		return
	}
	// file state=absent cannot be expressed in a manifest (absence has no hash).
	if mod == "file" {
		if state, _ := argMap["state"].(string); state == "absent" {
			*warnings = append(*warnings, fmt.Sprintf("task %q: file state=absent not trackable, skipped", taskName))
			return
		}
	}
	for _, arg := range args {
		raw, ok := argMap[arg].(string)
		if !ok || raw == "" {
			continue
		}
		for _, expanded := range expandPath(raw, vars, loop) {
			if expanded == "" {
				*warnings = append(*warnings, fmt.Sprintf("task %q: %s %s=%q left unresolved, skipped", taskName, mod, arg, raw))
				continue
			}
			if mod == "unarchive" {
				*warnings = append(*warnings, fmt.Sprintf("task %q: unarchive members under %s not enumerable, skipped", taskName, expanded))
				continue
			}
			if strings.HasSuffix(raw, "/") && mod == "get_url" {
				*warnings = append(*warnings, fmt.Sprintf("task %q: get_url directory dest %s has unknown filename, skipped", taskName, expanded))
				continue
			}
			*files = append(*files, expanded)
		}
		return // first present arg key wins
	}
}

// scanCommandCreates records creates= paths (files the task ensures exist).
// removes= paths express absence and are skipped with a warning.
func (s *scanner) scanCommandCreates(task map[string]any, taskName string, vars map[string]string, loop []string, files, warnings *[]string) {
	hasCmd := false
	oneLiners := []string{}
	for k, v := range task {
		switch shortModule(k) {
		case "command", "shell", "script":
			hasCmd = true
			if one, ok := v.(string); ok {
				oneLiners = append(oneLiners, one)
			}
		}
	}
	if !hasCmd {
		return
	}
	collect := func(raw string) {
		if raw == "" {
			return
		}
		for _, expanded := range expandPath(raw, vars, loop) {
			if expanded == "" {
				*warnings = append(*warnings, fmt.Sprintf("task %q: creates=%q left unresolved, skipped", taskName, raw))
				continue
			}
			*files = append(*files, expanded)
		}
	}
	if c, ok := task["creates"].(string); ok {
		collect(c)
	}
	if r, ok := task["removes"].(string); ok && r != "" {
		*warnings = append(*warnings, fmt.Sprintf("task %q: removes=%q expresses absence, not trackable, skipped", taskName, r))
	}
	// One-liner form: "shell: /bin/foo creates=/x".
	for _, one := range oneLiners {
		if m := createsRe.FindStringSubmatch(one); m != nil {
			collect(m[1])
		}
	}
}

// expandPath substitutes play vars and static loop items into raw.
// Returns one path per loop item, or [""] when unresolvable
// (templated without local vars, or not absolute).
func expandPath(raw string, vars map[string]string, loop []string) []string {
	sub := substituteVars(raw, vars)
	if !strings.Contains(sub, "{{ item }}") {
		if hasTemplate(sub) || !filepath.IsAbs(sub) {
			return []string{""}
		}
		return []string{sub}
	}
	if len(loop) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(loop))
	for _, item := range loop {
		p := strings.ReplaceAll(sub, "{{ item }}", item)
		if hasTemplate(p) || !filepath.IsAbs(p) {
			return []string{""}
		}
		out = append(out, p)
	}
	return out
}

var templateRe = regexp.MustCompile(`\{\{.*?\}\}`)

func hasTemplate(s string) bool { return templateRe.MatchString(s) }

// substituteVars replaces bare {{ name }} references with vars values.
func substituteVars(s string, vars map[string]string) string {
	return templateRe.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m, "{{"), "}}"))
		if v, ok := vars[name]; ok {
			return v
		}
		return m
	})
}

func shortModule(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// includeFileOf returns the referenced task file for static includes.
func includeFileOf(task map[string]any) string {
	for _, k := range []string{"import_tasks", "include_tasks", "ansible.builtin.import_tasks", "ansible.builtin.include_tasks"} {
		switch v := task[k].(type) {
		case string:
			return v
		case map[string]any:
			if f, ok := v["file"].(string); ok {
				return f
			}
		}
	}
	return ""
}

// loopItemsOf normalizes loop:/with_items: to scalar strings; nil when dynamic.
func loopItemsOf(task map[string]any) []string {
	raw, ok := task["loop"]
	if !ok {
		raw, ok = task["with_items"]
	}
	if !ok {
		return nil
	}
	items, ok := asStringListOK(raw)
	if !ok || len(items) == 0 {
		return nil
	}
	return items
}

func taskVarsOf(task map[string]any) map[string]string {
	out := map[string]string{}
	if m, ok := task["vars"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := scalarString(v); ok {
				out[k] = s
			}
		}
	}
	return out
}

func mergeVars(base, over map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func asStringList(v any) []string {
	items, _ := asStringListOK(v)
	return items
}

func asStringListOK(v any) ([]string, bool) {
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(l))
	for _, item := range l {
		s, ok := scalarString(item)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int, int64, float64, bool:
		return fmt.Sprintf("%v", t), true
	default:
		return "", false
	}
}

func displayName(task map[string]any) string {
	if n, ok := task["name"].(string); ok && n != "" {
		return n
	}
	return "unnamed"
}

// Dedupe removes duplicates while preserving order. Shared by the scanner and
// the CLI's file-list merge.
func Dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
