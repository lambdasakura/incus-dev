package runner

// ArgList はコマンド引数を、表示時に値を隠すべきものと区別しながら組み立てる。
//
// Command.Redact をindexで直接指定すると、追加位置の計算を誤っても何も起きず
// 静かにSecretが表示されてしまう。値を追加する時点で区別を宣言することで、
// その失敗様式を避ける。
type ArgList struct {
	args   []string
	redact []int
}

// Args は平文の引数からArgListを作る。
func Args(values ...string) *ArgList {
	return &ArgList{args: append([]string{}, values...)}
}

// Add は表示してよい引数を追加する。
func (a *ArgList) Add(values ...string) *ArgList {
	a.args = append(a.args, values...)
	return a
}

// AddSecret は表示時に値を隠す引数を追加する。
func (a *ArgList) AddSecret(values ...string) *ArgList {
	for _, v := range values {
		a.redact = append(a.redact, len(a.args))
		a.args = append(a.args, v)
	}
	return a
}

// Apply は組み立てた引数をCommandへ設定する。
func (a *ArgList) Apply(c *Command) {
	c.Args = a.args
	c.Redact = a.redact
}
