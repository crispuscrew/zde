package capture

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const fullDisk = "ZDE_TEST_FULL_DISK_CAPTURE"

// The size limit runs in a child because RLIMIT_FSIZE is process-wide and
// would otherwise also constrain the Go test harness's files.
func TestAFileTheCompositorCouldNotFinishIsNotACapture(t *testing.T) {
	if into, ok := os.LookupEnv(fullDisk); ok {
		captureOntoAFullDisk(t, into)
		return
	}
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	child.Env = append(os.Environ(), fullDisk+"="+dir)
	out, err := child.CombinedOutput()
	if bytes.Contains(out, []byte("--- SKIP")) {
		t.Skipf("the write could not be made to fail on this machine:\n%s", out)
	}
	if err != nil {
		t.Fatalf("the process that wrote under the limit failed: %v\n%s", err, out)
	}
	if left := captureNamesIn(t, dir); len(left) != 0 {
		t.Errorf("a save that ran out of room published %q as a capture", left)
	}
}

func captureOntoAFullDisk(t *testing.T, dir string) {
	quick(t)
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	var previous unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &previous); err != nil {
		t.Skipf("this machine will not say what its file size limit is: %v", err)
	}
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 64, Max: previous.Max}); err != nil {
		t.Skipf("this machine will not take a file size limit: %v", err)
	}
	var writeErr error
	fake := newFake(func(path string) (string, bool) {
		writeErr = os.WriteFile(path, bytes.Repeat([]byte("pixels"), 500), 0o600)
		if writeErr != nil {
			return "", true
		}
		return path, true
	})
	_, shotErr := Shot(fake, Full, dir, noon)
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &previous); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(writeErr, syscall.EFBIG) {
		t.Fatalf("the save stopped for a reason this test did not arrange: %v", writeErr)
	}
	if shotErr == nil {
		t.Fatal("a capture that ran out of room reported success")
	}
}
