package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/crispuscrew/zde/internal/pass"
)

// The command line into the derivation (internal/pass). No daemon: a password
// is a function of the master, the pepper file and what the store says about
// the site, so nothing about it crosses the zded socket or leaves this process.
//
// What is not here, and is 0.2's other half: the trusted window and type-out
// (docs/vision.md, section 3). `pass.open` in the keymap spawns `zde pass`,
// which is still a command nothing answers, and the palette says so.
//
// The no-network container the vision asks for goes with that window and not
// with this. A zinc container is a thing an app is launched into, and what
// wants one is the long-lived surface that holds a master under a keyboard
// grab - not a verb in the session's own process that opens no socket, reads
// two files and exits.

// passCmd runs `zde pass <verb>`.
func passCmd(args []string) error {
	switch args[0] {
	case "init":
		if len(args) == 1 {
			return passInit()
		}
	case "list":
		if len(args) == 1 {
			return passList()
		}
	case "add":
		if len(args) >= 2 {
			return passAdd(args[1], args[2:])
		}
	case "alias":
		if len(args) == 3 {
			return passAlias(args[1], args[2])
		}
	case "rotate":
		if len(args) == 2 {
			return passCounter(args[1], "")
		}
	case "counter":
		if len(args) == 3 {
			return passCounter(args[1], args[2])
		}
	case "upgrade":
		if len(args) == 2 {
			return passUpgrade(args[1])
		}
	case "get":
		if len(args) >= 2 {
			return passGet(args[1], args[2:])
		}
	}
	usage()
	return fmt.Errorf("zde: unknown command %q", "pass "+strings.Join(args, " "))
}

func passInit() error {
	path := pass.PepperPath()
	if err := pass.CreatePepper(path); err != nil {
		return err
	}
	fmt.Printf("pepper written to %s\n", path)
	fmt.Println("back it up: every password zde derives here needs this file and it exists nowhere else")
	return nil
}

func passList() error {
	store, err := pass.LoadStore(pass.StorePath())
	if err != nil {
		return err
	}
	if len(store.Sites) == 0 {
		fmt.Printf("no services yet: `zde pass add NAME` records one (%s)\n", pass.StorePath())
		return nil
	}
	for _, site := range store.Sites {
		name := site.Service
		if len(site.Aliases) > 0 {
			name += " (" + strings.Join(site.Aliases, ", ") + ")"
		}
		fmt.Printf("%s\n  counter %d, %s\n  %s\n", name, site.Counter, site.Policy.Describe(), params(site.Params))
	}
	return nil
}

func params(p pass.Params) string {
	return fmt.Sprintf("argon2id v%d t=%d m=%dKiB p=%d", p.Version, p.Time, p.MemoryKiB, p.Lanes)
}

func passAdd(name string, rest []string) error {
	pol := pass.DefaultPolicy()
	fs := flags("pass add")
	length := fs.Int("length", pol.Length, "how many characters the site takes")
	classes := fs.String("classes", strings.Join(pol.Allow, ","), "the character classes the site takes, one of each required")
	exclude := fs.String("exclude", "", "characters the site refuses")
	if err := parse(fs, rest); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("zde pass add takes one service name, and %q came after it", fs.Arg(0))
	}
	pol.Length = *length
	pol.Allow = nil
	pol.Require = map[string]int{}
	for _, c := range strings.Split(*classes, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		pol.Allow = append(pol.Allow, c)
		pol.Require[c] = 1
	}
	pol.Exclude = *exclude

	path := pass.StorePath()
	store, err := pass.LoadStore(path)
	if err != nil {
		return err
	}
	site := &pass.Site{Service: pass.Canonical(name), Counter: 1, Params: pass.Default(), Policy: pol}
	if err := store.Add(site); err != nil {
		return err
	}
	if err := pass.Save(path, store); err != nil {
		return err
	}
	fmt.Printf("%s: counter %d, %s\n", site.Service, site.Counter, site.Policy.Describe())
	return nil
}

func passAlias(name, alias string) error {
	path := pass.StorePath()
	store, err := pass.LoadStore(path)
	if err != nil {
		return err
	}
	site, err := store.Find(name)
	if err != nil {
		return err
	}
	if err := store.Alias(site, alias); err != nil {
		return err
	}
	if err := pass.Save(path, store); err != nil {
		return err
	}
	fmt.Printf("%s is also %s, and both derive the same password\n", site.Service, pass.Canonical(alias))
	return nil
}

// passCounter moves a site's counter: `rotate` to the next one, `counter N` to
// a number.
//
// Both print where it was, and that is the whole safety net - it is also
// enough. A counter is a number and the derivation is a function of it, so an
// increment nobody meant costs one command to undo and loses nothing: the old
// password is still there at the old number, and `zde pass get NAME -counter N`
// derives it without touching the store at all.
func passCounter(name, to string) error {
	path := pass.StorePath()
	store, err := pass.LoadStore(path)
	if err != nil {
		return err
	}
	site, err := store.Find(name)
	if err != nil {
		return err
	}
	was := site.Counter
	next := was + 1
	if to != "" {
		n, err := strconv.ParseUint(to, 10, 64)
		if err != nil || n == 0 {
			return fmt.Errorf("%q is not a counter: they start at 1 and go up", to)
		}
		next = n
	}
	site.Counter = next
	if err := pass.Save(path, store); err != nil {
		return err
	}
	fmt.Printf("%s: counter %d -> %d\n", site.Service, was, next)
	fmt.Printf("the old password is still `zde pass get %s -counter %d`, so change it at the site before you need it\n", site.Service, was)
	return nil
}

// passUpgrade re-stamps one site with today's parameters, which changes its
// password.
func passUpgrade(name string) error {
	path := pass.StorePath()
	store, err := pass.LoadStore(path)
	if err != nil {
		return err
	}
	site, err := store.Find(name)
	if err != nil {
		return err
	}
	was := site.Params
	if !site.Restamp(pass.Default()) {
		fmt.Printf("%s is already on %s\n", site.Service, params(was))
		return nil
	}
	if err := pass.Save(path, store); err != nil {
		return err
	}
	fmt.Printf("%s: %s -> %s\n", site.Service, params(was), params(site.Params))
	fmt.Println("this is a different password: change it at the site now")
	return nil
}

// showEnv is the second half of the gate on printing a derived password. See
// passGet.
const showEnv = "ZDE_PASS_SHOW_SECRET"

func passGet(name string, rest []string) error {
	fs := flags("pass get")
	counter := fs.Uint64("counter", 0, "derive an older rotation instead of the current one")
	show := fs.Bool("show-secret", false, "print the password on stdout (needs "+showEnv+"=1; there is no type-out yet)")
	if err := parse(fs, rest); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("zde pass get takes one service name, and %q came after it", fs.Arg(0))
	}
	store, err := pass.LoadStore(pass.StorePath())
	if err != nil {
		return err
	}
	site, err := store.Find(name)
	if err != nil {
		return err
	}
	n := site.Counter
	if *counter != 0 {
		n = *counter
	}
	pepper, err := pass.ReadPepper(pass.PepperPath())
	if err != nil {
		return err
	}
	defer pass.Zero(pepper)
	master, err := readSecretBytes("master password: ")
	if err != nil {
		return err
	}
	defer pass.Zero(master)
	secret, err := pass.Derive(master, pepper, site.Service, n, site.Params, site.Policy)
	if err != nil {
		return err
	}
	defer pass.Zero(secret)
	if !*show {
		// The answer with no secret in it. It says the derivation ran and under
		// what, which is what there is to check until the window can type the
		// result somewhere.
		fmt.Printf("%s: derived %d characters, counter %d, %s\n", site.Service, len(secret), n, params(site.Params))
		fmt.Printf("nothing was printed and nothing was copied: -show-secret with %s=1 prints it, until type-out lands\n", showEnv)
		return nil
	}
	// Two gates, and neither is the other's spare. A flag alone is a line in
	// somebody's shell history that prints a password the next time it is
	// recalled; an environment variable alone is a thing left set in a profile,
	// which would make every `zde pass get` print a secret. This whole path is
	// temporary: it exists because the window that types a password into a
	// field is not written yet (docs/vision.md, section 3), and it goes when
	// that lands. What it must never become is the clipboard, which is
	// principle 5 and the one thing pass exists to avoid.
	if os.Getenv(showEnv) != "1" {
		return fmt.Errorf("-show-secret puts a password on this terminal, so it also needs %s=1 in the environment: it is the development path until pass can type into a field", showEnv)
	}
	fmt.Fprintf(os.Stderr, "%s: printing a password on a terminal, which the trusted window will replace\n", site.Service)
	os.Stdout.Write(secret) //nolint:errcheck // a write to stdout that fails has nowhere to report it
	fmt.Println()
	return nil
}

// flags is a flag set that reports what it did not understand as an error
// rather than printing Go's own usage over zde's and exiting the process
// underneath its caller.
func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse names the command that did not understand, which the flag package's own
// message does not.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("zde %s: %w - `zde --help` lists what it takes", fs.Name(), err)
	}
	return nil
}
