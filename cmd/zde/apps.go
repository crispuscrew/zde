package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/crispuscrew/zde/internal/apps"
)

// launch replaces this process so no CLI parent waits behind a long-lived app.
func launch(name string) error {
	all, err := apps.Load(apps.DefaultPath())
	if err != nil {
		return err
	}
	argv, err := all.Argv(name)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("%s is configured to run %q, which is not there: %w", name, argv[0], err)
	}
	return syscall.Exec(bin, argv, os.Environ())
}

func appList() error {
	all, err := apps.Load(apps.DefaultPath())
	if err != nil {
		return err
	}
	names := all.Names()
	if len(names) == 0 {
		return fmt.Errorf("nothing is configured to run: set zde.apps in your home-manager config")
	}
	for _, n := range names {
		argv, _ := all.Argv(n)
		fmt.Printf("%s\t%s\n", n, strings.Join(argv, " "))
	}
	return nil
}
