package main

import (
	"fmt"
	"os"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/doctor"
)

// Set by nix/zde.nix with -X main.version; local builds report dev.
var version = "dev"

// runDoctor prints the whole report; only failed checks make the exit non-zero.
func runDoctor() error {
	report := doctor.Run()
	fmt.Print(report)
	if n := report.Failed(); n > 0 {
		return fmt.Errorf("zde doctor: %d of %d checks failed", n, len(report))
	}
	return nil
}

// runReport falls back to stdout without failing the login unit. Report readings
// are filtered individually by doctor; the filesystem error needs Block here.
func runReport() error {
	path, text, err := doctor.WriteReport(doctor.Self{Zde: version})
	if err == nil {
		fmt.Println("wrote", path)
		return nil
	}
	fmt.Print(text)
	fmt.Fprintf(os.Stderr, "\nno file written: %s\n"+
		"%s belongs to root and is made by layer 0 when zde.debug is on (nix/system.nix).\n",
		attn.Block(err.Error()), doctor.ReportDir)
	return nil
}
