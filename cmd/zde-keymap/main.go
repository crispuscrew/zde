// zde-keymap: keymap.yaml -> niri binds (KDL) + cheatsheet (markdown).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/crispuscrew/zde/internal/keymap"
)

func main() {
	in := flag.String("in", "common/keymap/keymap.yaml", "keymap source")
	kdl := flag.String("kdl", "", "write the niri binds here (default stdout)")
	cheat := flag.String("cheatsheet", "", "write the markdown cheatsheet here")
	check := flag.Bool("check", false, "validate the keymap and exit")
	flag.Parse()

	km, err := keymap.Load(*in)
	if err != nil {
		fatal(err)
	}
	if *check {
		fmt.Printf("%s: %d binds ok\n", *in, len(km.Binds))
		return
	}
	if err := out(*kdl, keymap.EmitKDL(km)); err != nil {
		fatal(err)
	}
	if *cheat != "" {
		if err := out(*cheat, keymap.EmitCheatsheet(km)); err != nil {
			fatal(err)
		}
	}
}

func out(path, content string) error {
	if path == "" {
		fmt.Print(content)
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "zde-keymap:", err)
	os.Exit(1)
}
