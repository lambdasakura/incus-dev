package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Step is one bootstrap or provision step. Run and Ansible are mutually
// exclusive; validation reports both being nil, and both being set.
type Step struct {
	Name    string
	Run     *RunStep
	Ansible *AnsibleStep
	Galaxy  *GalaxyStep
}

// RunStep runs a command inside the container.
type RunStep struct {
	Script string
	Shell  string
	Cwd    string
	User   string
	Env    map[string]string
}

// ShellOrDefault returns the shell that interprets the script.
func (r *RunStep) ShellOrDefault() string {
	if r.Shell == "" {
		return DefaultShell
	}
	return r.Shell
}

// AnsibleStep runs ansible-playbook on the host.
type AnsibleStep struct {
	Playbook  string   `json:"playbook"`
	Vars      string   `json:"vars,omitempty"`
	Inventory string   `json:"inventory,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	SkipTags  []string `json:"skip_tags,omitempty"`
	ExtraArgs []string `json:"extra_args,omitempty"`
}

// GalaxyStep runs ansible-galaxy install on the host.
//
// It lets a project install its own Ansible roles and collections without
// help from idev.
type GalaxyStep struct {
	Requirements string   `json:"requirements"`
	ExtraArgs    []string `json:"extra_args,omitempty"`
}

// Kind names which of the three a step is, or "" when it is none of them.
//
// One place decides it, because the answer is printed by `provision --list`
// and by `up --dry-run` and the two must agree. A default arm would name a
// step that is none of them after whichever kind the arm happened to pick,
// which is what a fourth kind would look like everywhere it was missed --
// galaxy was added after ansible, so a fourth is not hypothetical.
func (s Step) Kind() string {
	switch {
	case s.Run != nil:
		return "run"
	case s.Ansible != nil:
		return "ansible"
	case s.Galaxy != nil:
		return "galaxy"
	}
	return ""
}

// DisplayName returns the name to show in logs. index is 1-based.
func (s Step) DisplayName(index int) string {
	if s.Name != "" {
		return s.Name
	}
	return fmt.Sprintf("step %d", index)
}

// stepJSON is the intermediate form that accepts both the short run form and
// the full form.
type stepJSON struct {
	Name    string       `json:"name"`
	Run     *string      `json:"run"`
	Shell   string       `json:"shell"`
	Cwd     string       `json:"cwd"`
	User    string       `json:"user"`
	Env     StringMap    `json:"env"`
	Ansible *AnsibleStep `json:"ansible"`
	Galaxy  *GalaxyStep  `json:"galaxy"`
}

// UnmarshalJSON decodes a step.
//
// Semantic problems, such as run, ansible and galaxy being mutually exclusive,
// are not errors here; validation reports them with their position
// (spec 07-implementation.md 7.3.4).
func (s *Step) UnmarshalJSON(b []byte) error {
	var raw stepJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	s.Name = raw.Name

	// When the kind is not exactly one, validation reports it with a position.
	kinds := 0
	for _, present := range []bool{raw.Run != nil, raw.Ansible != nil, raw.Galaxy != nil} {
		if present {
			kinds++
		}
	}
	if kinds != 1 {
		return nil
	}

	switch {
	case raw.Run != nil:
		s.Run = &RunStep{
			Script: *raw.Run,
			Shell:  raw.Shell,
			Cwd:    raw.Cwd,
			User:   raw.User,
			Env:    raw.Env,
		}
	case raw.Ansible != nil:
		s.Ansible = raw.Ansible
	default:
		s.Galaxy = raw.Galaxy
	}
	return nil
}

// StringMap is a map that normalises scalar values to strings (spec 3.6.4).
type StringMap map[string]string

// UnmarshalJSON accepts a string, a number or a boolean, and keeps a string.
func (m *StringMap) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make(StringMap, len(raw))
	for k, v := range raw {
		s, err := scalarString(v)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		out[k] = s
	}
	*m = out
	return nil
}

func scalarString(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	switch t := v.(type) {
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	case bool:
		return strconv.FormatBool(t), nil
	default:
		return "", fmt.Errorf("expected string, number or boolean")
	}
}
