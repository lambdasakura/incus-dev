package runner_test

import (
	"strings"
	"testing"

	"github.com/lambdasakura/incus-devkit/internal/runner"
)

// Building the arguments and marking them for masking together avoids
// miscalculating an index.
func TestArgList(t *testing.T) {
	a := runner.Args("exec", "dev-x")
	a.Add("--cwd", "/workspace")
	a.Add("--env")
	a.AddSecret("TOKEN=s3cret")
	a.Add("--")
	a.AddSecret("sh", "-c", "echo $TOKEN")

	var c runner.Command
	c.Name = "incus"
	a.Apply(&c)

	wantArgs := []string{"exec", "dev-x", "--cwd", "/workspace", "--env", "TOKEN=s3cret", "--", "sh", "-c", "echo $TOKEN"}
	if len(c.Args) != len(wantArgs) {
		t.Fatalf("Args = %v, want %v", c.Args, wantArgs)
	}
	for i := range wantArgs {
		if c.Args[i] != wantArgs[i] {
			t.Errorf("Args[%d] = %q, want %q", i, c.Args[i], wantArgs[i])
		}
	}

	display := c.String()
	for _, secret := range []string{"s3cret", "echo $TOKEN"} {
		if strings.Contains(display, secret) {
			t.Errorf("String() = %q, want it not to contain %q", display, secret)
		}
	}
	for _, want := range []string{"incus exec dev-x", "--cwd /workspace", "TOKEN=***"} {
		if !strings.Contains(display, want) {
			t.Errorf("String() = %q, want it to contain %q", display, want)
		}
	}
}

func TestArgListEmpty(t *testing.T) {
	var c runner.Command
	runner.Args().Apply(&c)

	if len(c.Args) != 0 || len(c.Redact) != 0 {
		t.Errorf("Args = %v, Redact = %v, want empty", c.Args, c.Redact)
	}
}

func TestArgListAppendsToExistingCommand(t *testing.T) {
	a := runner.Args("list")
	a.AddSecret("secret-filter")

	c := runner.Command{Name: "incus"}
	a.Apply(&c)

	if got := c.String(); strings.Contains(got, "secret-filter") {
		t.Errorf("String() = %q", got)
	}
}
