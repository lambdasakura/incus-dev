//go:build integration

package integration_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ansible ステップがSSHなしでコンテナへ適用されること（REQ-004）
func TestAnsibleProvisioning(t *testing.T) {
	requireCommand(t, "ansible-playbook")
	if err := exec.Command("ansible-doc", "-t", "connection", "community.general.incus").Run(); err != nil {
		t.Skip("community.general collection が無いためスキップします")
	}

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

	// devkitが注入する変数とプロジェクト側の変数の両方を使う。
	// ネットワークを必要としないタスクのみで構成する。
	writeFile(t, filepath.Join(f.root, ".incus-dev", "ansible", "site.yml"), `---
- name: Provision development environment
  hosts: dev
  gather_facts: true

  tasks:
    - name: Write marker
      ansible.builtin.copy:
        content: "{{ devkit_project_name }} {{ greeting }}\n"
        dest: /etc/idev-ansible-marker
        mode: "0644"

    - name: Ensure workspace is visible
      ansible.builtin.stat:
        path: "{{ devkit_workspace }}/src/marker.txt"
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
		t.Errorf("marker = %q, devkit変数が渡っていない", got)
	}
	if !strings.Contains(got, "konnichiwa") {
		t.Errorf("marker = %q, プロジェクトのvarsが渡っていない", got)
	}

	// SSH Serverを導入していないこと（REQ-004）
	if _, err := f.run("shell", "--", "sh", "-c", "command -v sshd"); err == nil {
		t.Error("sshd が導入されている")
	}

	// 再実行できること（REQ-005）
	f.mustRun("provision")
}

// bootstrap: [] は既定bootstrapを無効化する（仕様 06-provisioning.md 6.3.3）
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

	// alpineには python3 が無いため、既定bootstrapが動いていれば失敗しているはず
	if _, err := f.run("shell", "--", "sh", "-c", "command -v python3"); err == nil {
		t.Error("bootstrap: [] を指定したのに python3 が導入されている")
	}
}

// bootstrap は provision より先に実行される（仕様 06-provisioning.md 6.1）
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
