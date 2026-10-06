package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlay(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("write play: %v", err)
	}
	return p
}

func TestScanPlaybookBasic(t *testing.T) {
	dir := t.TempDir()
	p := writePlay(t, dir, "site.yml", `
- hosts: all
  tasks:
    - name: nginx config
      ansible.builtin.template:
        src: nginx.conf.j2
        dest: /etc/nginx/nginx.conf
    - name: motd
      copy:
        content: "hi"
        dest: /etc/motd
    - name: ensure dir
      file:
        path: /var/lib/app
        state: directory
`)
	files, warnings, err := ScanPlaybooks([]string{p})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: got %v, want none", warnings)
	}
	want := map[string]bool{"/etc/nginx/nginx.conf": true, "/etc/motd": true, "/var/lib/app": true}
	if len(files) != len(want) {
		t.Fatalf("files: got %v", files)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file %q", f)
		}
	}
}

func TestScanPlaybookVarsLoopIncludes(t *testing.T) {
	dir := t.TempDir()
	writePlay(t, dir, "extra.yml", `
- name: extra file
  copy:
    dest: "{{ conf_dir }}/extra.conf"
    content: x
`)
	p := writePlay(t, dir, "site.yml", `
- hosts: all
  vars:
    conf_dir: /etc/myapp
  tasks:
    - name: main config
      template:
        dest: "{{ conf_dir }}/main.conf"
        src: x.j2
    - name: looped
      copy:
        dest: "{{ item }}/app.conf"
        content: x
      loop:
        - /etc/a
        - /etc/b
    - import_tasks: extra.yml
    - name: absent file
      file:
        path: /tmp/gone
        state: absent
    - name: dynamic include
      include_tasks: "{{ role_file }}"
    - name: templated dest
      copy:
        dest: "{{ unknown_var }}/x.conf"
        content: x
`)
	files, warnings, err := ScanPlaybooks([]string{p})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	want := map[string]bool{
		"/etc/myapp/main.conf":  true,
		"/etc/a/app.conf":       true,
		"/etc/b/app.conf":       true,
		"/etc/myapp/extra.conf": true,
	}
	if len(files) != len(want) {
		t.Fatalf("files: got %v, want %v", files, want)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file %q", f)
		}
	}
	if len(warnings) != 3 {
		t.Errorf("warnings: got %v, want 3 (absent, dynamic include, unknown var)", warnings)
	}
}

func TestScanPlaybookBlocksAndCreates(t *testing.T) {
	dir := t.TempDir()
	p := writePlay(t, dir, "site.yml", `
- hosts: all
  tasks:
    - block:
        - name: nested line
          lineinfile:
            path: /etc/ssh/sshd_config
            line: "PermitRootLogin no"
        - name: installer
          shell: /opt/install.sh creates=/opt/installed.marker
      rescue:
        - name: fallback config
          copy:
            dest: /etc/app/fallback.conf
            content: x
`)
	files, _, err := ScanPlaybooks([]string{p})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	want := map[string]bool{
		"/etc/ssh/sshd_config":   true,
		"/opt/installed.marker":  true,
		"/etc/app/fallback.conf": true,
	}
	if len(files) != len(want) {
		t.Fatalf("files: got %v", files)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file %q", f)
		}
	}
}

func TestScanPlaybookMissingFile(t *testing.T) {
	if _, _, err := ScanPlaybooks([]string{filepath.Join(t.TempDir(), "nope.yml")}); err == nil {
		t.Error("missing playbook: want error, got nil")
	}
}
