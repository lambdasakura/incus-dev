//go:build integration

package integration_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An ansible step reaches the container without SSH (REQ-004).
func TestAnsibleProvisioning(t *testing.T) {
	requireCommand(t, "ansible-playbook")
	requireConnectionPlugin(t)

	f := newFixture(t, `
schema: 1
project:
  name: {{PROJECT}}
instance:
  image: {{ANSIBLE_IMAGE}}
workspace:
  idmap: none
provision:
  - name: apply playbook
    ansible:
      playbook: .incus-dev/ansible/site.yml
      vars: .incus-dev/ansible/vars.yml
`)

	// Use both the variables idev injects and the project's own. Keep to
	// tasks that need no network.
	writeFile(t, filepath.Join(f.root, ".incus-dev", "ansible", "site.yml"), `---
- name: Provision development environment
  hosts: dev
  gather_facts: true

  tasks:
    - name: Write marker
      ansible.builtin.copy:
        content: "{{ idev_project_name }} {{ greeting }}\n"
        dest: /etc/idev-ansible-marker
        mode: "0644"

    - name: Ensure workspace is visible
      ansible.builtin.stat:
        path: "{{ idev_workspace }}/src/marker.txt"
      register: ws

    - name: Fail when workspace is missing
      ansible.builtin.fail:
        msg: "workspace not mounted"
      when: not ws.stat.exists
`)
	writeFile(t, filepath.Join(f.root, ".incus-dev", "ansible", "vars.yml"), "greeting: konnichiwa\n")

	f.mustRun("up")

	got := f.mustRun("shell", "--", "cat", "/etc/idev-ansible-marker")
	if !strings.Contains(got, f.project) {
		t.Errorf("marker = %q, the idev variables never arrived", got)
	}
	if !strings.Contains(got, "konnichiwa") {
		t.Errorf("marker = %q, the project's vars never arrived", got)
	}

	// No SSH server was installed (REQ-004).
	if _, err := f.run("shell", "--", "sh", "-c", "command -v sshd"); err == nil {
		t.Error("sshd was installed")
	}

	// It can be run again (REQ-005).
	f.mustRun("provision")
}

// The default bootstrap installs Python and the ansible step works
// (spec 06-provisioning.md 6.3.2; the sole exception to REQ-007).
func TestDefaultBootstrapInstallsPython(t *testing.T) {
	requireCommand(t, "ansible-playbook")
	requireConnectionPlugin(t)

	// Use a Debian-family image without python3.
	f := newFixture(t, `
schema: 1
project:
  name: {{PROJECT}}
instance:
  image: `+bootstrapImage+`
workspace:
  idmap: shift
provision:
  - name: apply playbook
    ansible:
      playbook: .incus-dev/ansible/site.yml
`)
	writeFile(t, filepath.Join(f.root, ".incus-dev", "ansible", "site.yml"), `---
- name: Provision
  hosts: dev
  gather_facts: false

  tasks:
    - name: Write marker
      ansible.builtin.copy:
        content: "bootstrapped
"
        dest: /etc/idev-bootstrap-marker
        mode: "0644"
`)

	f.mustRun("up")

	if got := f.mustRun("shell", "--", "cat", "/etc/idev-bootstrap-marker"); !strings.Contains(got, "bootstrapped") {
		t.Errorf("marker = %q", got)
	}
	if _, err := f.run("shell", "--", "sh", "-c", "command -v python3"); err != nil {
		t.Error("the default bootstrap did not install python3")
	}
}

// bootstrap: [] disables the default bootstrap (spec 06-provisioning.md 6.3.3).
func TestBootstrapCanBeDisabled(t *testing.T) {
	f := newFixture(t, `
schema: 1
project:
  name: {{PROJECT}}
instance:
  image: {{IMAGE}}
workspace:
  idmap: none
bootstrap: []
provision:
  - run: test -f /workspace/src/marker.txt
`)
	f.mustRun("up")

	// alpine has no python3, so the default bootstrap would have failed had it
	// run.
	if _, err := f.run("shell", "--", "sh", "-c", "command -v python3"); err == nil {
		t.Error("python3 was installed despite bootstrap: []")
	}
}

// bootstrap runs before provisioning (spec 06-provisioning.md 6.1).
func TestBootstrapRunsBeforeProvision(t *testing.T) {
	f := newFixture(t, `
schema: 1
project:
  name: {{PROJECT}}
instance:
  image: {{IMAGE}}
workspace:
  idmap: none
bootstrap:
  - name: prepare
    run: echo bootstrap > /etc/idev-order
provision:
  - name: check order
    run: |
      grep -q bootstrap /etc/idev-order
      echo provision >> /etc/idev-order
`)
	f.mustRun("up")

	got := f.mustRun("shell", "--", "cat", "/etc/idev-order")
	if !strings.Contains(got, "bootstrap") || !strings.Contains(got, "provision") {
		t.Errorf("order = %q", got)
	}
}

// requireConnectionPlugin skips unless the connection plugin idev uses is
// installed.
//
// ansible-doc exits 0 for a plugin it cannot find, writing its warning to
// standard error, so the exit code says only that ansible-doc ran. Reading it
// alone made this guard never skip, and the test then built an instance and
// failed late instead.
func requireConnectionPlugin(t *testing.T) {
	t.Helper()

	out, err := exec.Command("ansible-doc", "-t", "connection", "community.general.incus").Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		t.Skip("skipping: the community.general collection is not installed")
	}
}
