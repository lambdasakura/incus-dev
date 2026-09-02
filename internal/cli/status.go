package cli

// Status: what idev reports about an instance, as text and as JSON
// (spec 04-cli.md 4.9).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
)

// statusReport is what status prints.
type statusReport struct {
	Project  string `json:"project"`
	Instance string `json:"instance"`
	Status   string `json:"status"`
	Image    string `json:"image"`
	// ImageDeclared is what dev.yml asks for, set only when the instance was
	// made from something else.
	ImageDeclared string `json:"image_declared,omitempty"`
	Workspace     string `json:"workspace"`
	Source        string `json:"workspace_source"`
	// SourceDeclared is what dev.yml points at, set only when another
	// checkout's tree is the one actually mounted.
	SourceDeclared string            `json:"workspace_source_declared,omitempty"`
	Exists         bool              `json:"exists"`
	Managed        bool              `json:"managed"`
	Profiles       []string          `json:"profiles,omitempty"`
	Config         map[string]string `json:"config,omitempty"`
	Devices        []string          `json:"devices,omitempty"`
	Steps          int               `json:"provision_steps"`
	Runtime        string            `json:"runtime,omitempty"`
	IncusProject   string            `json:"incus_project"`
}

// Status prints the state of the instance.
func (a *App) Status(ctx context.Context, asJSON bool) error {
	ws := a.cfg.WorkspaceOrDefault()
	report := statusReport{
		Project:      a.cfg.Project.Name,
		Instance:     a.instance,
		Status:       "NOT CREATED",
		Image:        a.cfg.Instance.Image,
		Workspace:    ws.Target,
		Source:       a.cfg.WorkspaceSourcePath(),
		Steps:        len(a.cfg.Provision),
		IncusProject: a.incusProject,
	}
	if a.cfg.Runtime != nil {
		report.Runtime = a.cfg.Runtime.Version
	}

	inst, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		report.Exists = true
		report.Status = inst.Status
		// What is actually mounted, which is another checkout's tree when one
		// ran up more recently (spec 04-cli.md 4.4).
		if src := inst.Devices[config.WorkspaceDeviceName]["source"]; src != "" {
			report.Source = src
			if src != a.cfg.WorkspaceSourcePath() {
				report.SourceDeclared = a.cfg.WorkspaceSourcePath()
			}
		}
		// What the instance was made from, which up does not change. The
		// declaration is shown beside it when the two disagree, so the row
		// never quietly means one thing or the other.
		if was := inst.Config[managedImageKey]; was != "" {
			report.Image = was
			if was != a.cfg.Instance.Image {
				report.ImageDeclared = a.cfg.Instance.Image
			}
		} else {
			// An instance made before the record. Showing the declaration as
			// the instance's image would say it was made from it, which is
			// the one thing this row is for and the one thing idev cannot
			// know; imageRow says so instead.
			report.Image = ""
			report.ImageDeclared = a.cfg.Instance.Image
		}
		report.Managed = isManagedBy(inst.Config, a.cfg.Project.Name)
		report.Profiles = inst.Profiles
		report.Config = limitsOf(inst.Config)
		report.Devices = deviceSummary(inst.Devices)
	case !errors.Is(err, incus.ErrInstanceNotFound):
		return err
	}

	if asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return a.printStatus(report)
}

func (a *App) printStatus(r statusReport) error {
	rows := [][2]string{
		{"Project", r.Project},
		{"Instance", r.Instance},
		{"Status", r.Status},
		{"Image", imageRow(r)},
		{"Workspace", workspaceRow(r)},
	}
	if r.Exists {
		rows = append(rows,
			[2]string{"Profiles", strings.Join(r.Profiles, ", ")},
			[2]string{"Devices", strings.Join(r.Devices, ", ")},
			[2]string{"Managed", yesNo(r.Managed)},
		)
		for _, k := range sortedKeys(r.Config) {
			rows = append(rows, [2]string{k, r.Config[k]})
		}
	}
	rows = append(rows,
		[2]string{"Provision", fmt.Sprintf("%d step(s)", r.Steps)},
		[2]string{"Runtime", r.Runtime},
		[2]string{"Incus", orDefault(r.IncusProject, defaultIncusProject)},
	)

	for _, row := range rows {
		if row[1] == "" {
			continue
		}
		if _, err := fmt.Fprintf(a.out, "%-11s %s\n", row[0]+":", row[1]); err != nil {
			return err
		}
	}
	return nil
}

// deviceSummary renders devices as a list of "name(type)".
func deviceSummary(devices map[string]incus.Device) []string {
	out := make([]string, 0, len(devices))
	for _, name := range slices.Sorted(maps.Keys(devices)) {
		out = append(out, fmt.Sprintf("%s(%s)", name, devices[name].Type()))
	}
	return out
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// workspaceRow shows what is mounted, and the declaration when another
// checkout's tree is the one in the instance.
func workspaceRow(r statusReport) string {
	out := r.Source + " -> " + r.Workspace
	if r.SourceDeclared != "" {
		out += " (dev.yml points at " + r.SourceDeclared + "; idev up remounts it here)"
	}
	return out
}

// imageRow shows the image, and the declaration when it asks for another one.
func imageRow(r statusReport) string {
	switch {
	case r.ImageDeclared == "":
		return r.Image
	case r.Image == "":
		// Made before idev recorded it. The declaration is all there is, and
		// saying so is the difference between a fact and a guess.
		return r.ImageDeclared + " (declared; this instance predates the record, " +
			"so what it was made from is not recorded)"
	}
	return r.Image + " (dev.yml declares " + r.ImageDeclared + "; idev rebuild to recreate)"
}

// limitsOf picks out the config keys worth displaying.
func limitsOf(instanceConfig map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range instanceConfig {
		if strings.HasPrefix(k, "limits.") {
			out[k] = v
		}
	}
	return out
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
