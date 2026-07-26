// zde-keymap: keymap.yaml -> niri binds (KDL) + cheatsheet (markdown).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/crispuscrew/zde/internal/keymap"
)

func main() {
	in := flag.String("in", "common/keymap/keymap.yaml", "keymap source")
	kdl := flag.String("kdl", "", "write the niri binds here (default stdout)")
	cheat := flag.String("cheatsheet", "", "write the markdown cheatsheet here")
	check := flag.Bool("check", false, "validate the keymap and exit")
	flag.Parse()

	// The source is -in, never a positional: silently generating the default
	// keymap because an argument landed in the wrong place is worse than
	// stopping.
	if flag.NArg() != 0 {
		fatal(fmt.Errorf("unexpected argument %q (the source is -in)", flag.Arg(0)))
	}
	if *check && (*kdl != "" || *cheat != "") {
		fatal(fmt.Errorf("-check validates and writes nothing; drop -kdl and -cheatsheet"))
	}

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
		// Checked: a generator that writes nothing and exits 0 (a full disk, a
		// closed pipe) is the worst failure available to it.
		_, err := io.WriteString(os.Stdout, content)
		return err
	}
	// Written beside the target and renamed over it, so a failure part way
	// through cannot leave a truncated config in place - os.WriteFile truncates
	// first and would.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.WriteString(tmp, content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "zde-keymap:", err)
	os.Exit(1)
}
