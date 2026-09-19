package zded

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The current test binary doubles as the tier, avoiding dependencies on host programs.
const fakeTierMark = "zde-fake-tier"

// Separate chunks long enough to distinguish streaming from a buffered final answer.
const fakeTierGap = 400 * time.Millisecond

func fakeTier(mode string, extra ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestFakeTier$", "--", fakeTierMark, mode}, extra...)
}

// Only marked subprocesses act as tiers; exit before the test framework writes PASS.
func TestFakeTier(t *testing.T) {
	args := flag.Args()
	if len(args) < 2 || args[0] != fakeTierMark {
		return
	}
	code := 0
	switch args[1] {
	case "echo":
		q, _ := io.ReadAll(os.Stdin)
		fmt.Fprintf(os.Stdout, "answered: %s", q)
	case "stream":
		fmt.Fprint(os.Stdout, "one")
		time.Sleep(fakeTierGap)
		fmt.Fprint(os.Stdout, "two")
	case "dump":
		io.Copy(os.Stdout, os.Stdin)
	case "silent":
	case "slow":
		time.Sleep(2 * time.Second)
		fmt.Fprint(os.Stdout, "eventually")
	case "angry":
		fmt.Fprintln(os.Stderr, "no credentials in this container")
		code = 1
	case "cyrillic":
		fmt.Fprint(os.Stdout, strings.Repeat(cyrillicWord, cyrillicTimes))
	case "loop":
		for {
			if _, err := fmt.Fprint(os.Stdout, strings.Repeat("x", 4096)); err != nil {
				break
			}
		}
	case "fork":
		grand := exec.Command(os.Args[0], "-test.run=^TestFakeTier$", "--", fakeTierMark, "linger", args[2])
		grand.Stdout = os.Stdout
		grand.Stderr = os.Stderr
		if err := grand.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
			break
		}
		fmt.Fprint(os.Stdout, "answered and forked")
	case "linger":
		os.WriteFile(args[2], []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(30 * time.Second)
	}
	os.Exit(code)
}
