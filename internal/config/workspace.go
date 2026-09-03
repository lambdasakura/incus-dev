package config

// The workspace section reaches Go in one of two forms (spec 3.7), and leaves
// this file in one. Everything downstream reads Mounts and never learns which
// form the project wrote.

import (
	"encoding/json"
	"fmt"
)

// The keys that belong to the section rather than to a mount. idmap is a
// scalar and owner an object, which is why the form is decided by an object
// value that is not one of these.
const (
	idmapKey = "idmap"
	ownerKey = "owner"
)

// UnmarshalJSON reads either form of the workspace section (spec 3.7.2).
//
// The form is decided by whether any value is an object. Nothing here refuses
// anything: what it cannot use it leaves out, and validateWorkspaceShape
// reports it with the path the author wrote and a message saying what to do.
// A decoder that failed here would report the first problem only, and in a
// different shape from every other configuration error.
func (w *Workspace) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}

	if err := w.readOwner(raw); err != nil {
		return err
	}
	if !hasMountEntry(raw) {
		return w.unmarshalSingle(data)
	}
	return w.unmarshalMap(raw)
}

// unmarshalSingle reads the one-mount form, which becomes the entry named
// main.
func (w *Workspace) unmarshalSingle(data []byte) error {
	var single struct {
		Source   string    `json:"source,omitempty"`
		Target   string    `json:"target,omitempty"`
		Readonly bool      `json:"readonly,omitempty"`
		IDMap    IDMapMode `json:"idmap,omitempty"`
	}
	if err := json.Unmarshal(data, &single); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}

	w.IDMap = single.IDMap
	w.Mounts = map[string]Mount{MainMountName: {
		Source:   single.Source,
		Target:   single.Target,
		Readonly: single.Readonly,
	}}
	return nil
}

// unmarshalMap reads the map form, taking the entries it can and leaving the
// rest to validateWorkspaceShape.
func (w *Workspace) unmarshalMap(raw map[string]json.RawMessage) error {
	w.mapForm = true
	w.Mounts = make(map[string]Mount, len(raw))

	for _, name := range sortedKeys(raw) {
		value := raw[name]

		if name == idmapKey {
			// A wrong value is caught by the same validation that catches it
			// in the single form.
			_ = json.Unmarshal(value, &w.IDMap)
			continue
		}
		if name == ownerKey {
			continue // readOwner already took it.
		}
		if !isJSONObject(value) {
			continue
		}

		var mount Mount
		if err := json.Unmarshal(value, &mount); err != nil {
			return fmt.Errorf("workspace.%s: %w", name, err)
		}
		w.Mounts[name] = mount
	}
	return nil
}

// readOwner takes the section-level owner, if there is one.
//
// A wrong shape is left to validateWorkspaceShape, which reports it with the
// path the author wrote.
func (w *Workspace) readOwner(raw map[string]json.RawMessage) error {
	value, ok := raw[ownerKey]
	if !ok || !isJSONObject(value) {
		return nil
	}
	var owner Owner
	if err := json.Unmarshal(value, &owner); err != nil {
		return fmt.Errorf("workspace.owner: %w", err)
	}
	w.Owner = &owner
	return nil
}

// hasMountEntry reports whether any value is a mount, which is what tells the
// map form from the single one (spec 3.7.2).
//
// owner is an object too, and belongs to the section either way, so it is not
// what decides.
func hasMountEntry(raw map[string]json.RawMessage) bool {
	for name, value := range raw {
		if name != ownerKey && isJSONObject(value) {
			return true
		}
	}
	return false
}

// isJSONObject reports whether a raw value is a JSON object.
func isJSONObject(value json.RawMessage) bool {
	var probe any
	if err := json.Unmarshal(value, &probe); err != nil {
		return false
	}
	_, ok := probe.(map[string]any)
	return ok
}

// mount reports whether the section declares an entry of that name.
//
// A method on the section rather than on Config so a nil section answers
// without the caller checking, which is how validateVolumes reaches it.
func (w *Workspace) mount(name string) (Mount, bool) {
	if w == nil {
		return Mount{}, false
	}
	m, ok := w.Mounts[name]
	return m, ok
}
