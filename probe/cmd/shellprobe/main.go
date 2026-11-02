// shellprobe: mvdan.cc/sh/v3 纯 Go POSIX 解释器在 Windows 上的实测。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type tc struct {
	name string
	src  string
}

func main() {
	tmp, err := os.MkdirTemp("", "shprobe")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	cases := []tc{
		{"pipeline", `echo alpha beta gamma | tr ' ' '-'`},
		{"builtin_cd_export", `cd ` + bashQuote(tmp) + ` && export FOO=bar-baz && echo "cwd=$(pwd) FOO=$FOO"`},
		{"redirect_out", `printf 'line1\nline2\n' > ` + bashQuote(filepath.Join(tmp, "o.txt")) + ` ; cat ` + bashQuote(filepath.Join(tmp, "o.txt"))},
		{"redirect_append", `echo second >> ` + bashQuote(filepath.Join(tmp, "o.txt")) + ` ; wc -l < ` + bashQuote(filepath.Join(tmp, "o.txt"))},
		{"arith_cmdsub", `x=$((3*7)); echo "arith=$x sub=$(echo hi)-end"`},
		{"func_case_if", `f(){ case "$1" in a*) echo matched-a;; *) echo other;; esac; }; f abc; if [ 1 -lt 2 ]; then echo ifok; fi`},
		{"dbl_sqbracket", `[[ abc == a* ]] && echo bashdbracket-ok`},
		{"procsub", `cat <(echo from-procsub)`},
		{"arrays", `arr=(x y z); echo "${arr[1]}"`},
		{"win_path", `cd /tmp 2>/dev/null && pwd || echo "cd /tmp failed"`},
		{"set_pipefail", `set -o pipefail; false | true; echo "status=$?"`},
		{"no_external", `definitely_not_a_cmd_xyz --help`},
		{"glob", `cd ` + bashQuote(tmp) + ` && touch g1.txt g2.txt && echo g*.txt`},
	}

	pass, fail := 0, 0
	for _, c := range cases {
		fmt.Printf("\n### %s\n$ %s\n", c.name, c.src)
		out, err := run(c.src, tmp)
		if err != nil {
			fail++
			fmt.Printf("[ERR] %v\n", err)
			continue
		}
		pass++
		fmt.Print(out)
	}
	fmt.Printf("\nsummary: pass=%d fail=%d\n", pass, fail)
}

func run(src, dir string) (string, error) {
	f, err := syntax.NewParser().Parse(strings.NewReader(src), "probe.sh")
	if err != nil {
		return "", errors.New("parse: " + err.Error())
	}
	var sb strings.Builder
	r, err := interp.New(
		interp.StdIO(nil, &sb, &sb),
		interp.Env(expand.ListEnviron(os.Environ()...)),
		interp.Dir(dir),
	)
	if err != nil {
		return "", errors.New("interp.New: " + err.Error())
	}
	if err := r.Run(context.Background(), f); err != nil {
		return sb.String(), errors.New("run: " + err.Error())
	}
	return sb.String(), nil
}

func bashQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
