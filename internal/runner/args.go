package runner

// ArgList builds command arguments while keeping track of which ones must be
// hidden when displayed.
//
// Setting Command.Redact by index directly fails silently: get the position
// wrong and nothing happens except that a secret is printed. Declaring the
// distinction as the value is appended removes that failure mode.
type ArgList struct {
	args   []string
	redact []int
}

// Args builds an ArgList from plain arguments.
func Args(values ...string) *ArgList {
	return &ArgList{args: append([]string{}, values...)}
}

// Add appends arguments that are safe to display.
func (a *ArgList) Add(values ...string) *ArgList {
	a.args = append(a.args, values...)
	return a
}

// AddSecret appends arguments whose values are hidden when displayed.
func (a *ArgList) AddSecret(values ...string) *ArgList {
	for _, v := range values {
		a.redact = append(a.redact, len(a.args))
		a.args = append(a.args, v)
	}
	return a
}

// Apply writes the assembled arguments into a Command.
func (a *ArgList) Apply(c *Command) {
	c.Args = a.args
	c.Redact = a.redact
}
