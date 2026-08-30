package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Step は bootstrap / provision の1ステップ。
// Run と Ansible は排他であり、両方nilまたは両方非nilの状態はvalidationで報告される。
type Step struct {
	Name    string
	Run     *RunStep
	Ansible *AnsibleStep
}

// RunStep はコンテナ内でのコマンド実行。
type RunStep struct {
	Script string
	Shell  string
	Cwd    string
	User   string
	Env    map[string]string
}

// ShellOrDefault はスクリプトを解釈するシェルを返す。
func (r *RunStep) ShellOrDefault() string {
	if r.Shell == "" {
		return DefaultShell
	}
	return r.Shell
}

// AnsibleStep はホスト側での ansible-playbook 実行。
type AnsibleStep struct {
	Playbook  string   `json:"playbook"`
	Vars      string   `json:"vars,omitempty"`
	Inventory string   `json:"inventory,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	SkipTags  []string `json:"skip_tags,omitempty"`
	ExtraArgs []string `json:"extra_args,omitempty"`
}

// DisplayName はログ表示用の名前を返す。index は1始まり。
func (s Step) DisplayName(index int) string {
	if s.Name != "" {
		return s.Name
	}
	return fmt.Sprintf("step %d", index)
}

// Kind はステップ種別を返す。
func (s Step) Kind() string {
	switch {
	case s.Run != nil:
		return "run"
	case s.Ansible != nil:
		return "ansible"
	default:
		return "unknown"
	}
}

// stepJSON は run 短縮形とフル形式の両方を受けるための中間表現。
type stepJSON struct {
	Name    string       `json:"name"`
	Run     *string      `json:"run"`
	Shell   string       `json:"shell"`
	Cwd     string       `json:"cwd"`
	User    string       `json:"user"`
	Env     StringMap    `json:"env"`
	Ansible *AnsibleStep `json:"ansible"`
}

// UnmarshalJSON はステップをデコードする。
//
// run と ansible の排他性のような意味的な問題はここではエラーにせず、
// validation側で位置情報付きで報告する（仕様 07-implementation.md 7.3.4）。
func (s *Step) UnmarshalJSON(b []byte) error {
	var raw stepJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	s.Name = raw.Name
	hasRun := raw.Run != nil
	hasAnsible := raw.Ansible != nil

	if hasRun == hasAnsible {
		// どちらも無い / 両方ある場合はvalidationが報告する。
		return nil
	}

	if hasRun {
		s.Run = &RunStep{
			Script: *raw.Run,
			Shell:  raw.Shell,
			Cwd:    raw.Cwd,
			User:   raw.User,
			Env:    raw.Env,
		}
		return nil
	}
	s.Ansible = raw.Ansible
	return nil
}

// StringMap はスカラ値を文字列へ正規化するmap（仕様 3.6.4）。
type StringMap map[string]string

// UnmarshalJSON は string / number / boolean を文字列として受け取る。
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
