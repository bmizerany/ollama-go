package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Check calls t.Fatal(err) if err is not nil.
func Check(t *testing.T, err error) {
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
}

type CheckFunc func(err error)

// Checker returns a check function that
// calls t.Fatal if err is not nil.
func Checker(t *testing.T) (check func(err error)) {
	return func(err error) {
		if err != nil {
			t.Helper()
			t.Fatal(err)
		}
	}
}

// CheckTime calls t.Fatalf if got != want. Included in the error message is
// want.Sub(got) to help diagnose the difference, along with thier values in
// UTC.
func CheckTime(t *testing.T, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v (%v)", got.UTC(), want.UTC(), want.Sub(got))
	}
}

// WriteFile writes data to a file named name. It makes the directory if it
// doesn't exist and sets the file mode to perm.
//
// The name must be a relative path and must not contain .. or start with a /;
// otherwise WriteFile will panic.
func WriteFile[S []byte | string](t testing.TB, name string, data S) {
	t.Helper()

	if filepath.IsAbs(name) {
		t.Fatalf("WriteFile: name must be a relative path, got %q", name)
	}
	name = filepath.Clean(name)
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

// Chdir calls os.Chdir(dir) and uses Cleanup to restore the current
// working directory to its original value after the test. On Unix, it
// also sets PWD environment variable for the duration of the test.
//
// Because Chdir affects the whole process, it cannot be used in parallel tests
// or FuzzX tests.
//
// This will be standard in go1.24 and can be replaced with testing.T.Chdir.
func Chdir(c testing.TB, dir string) {
	oldwd, err := os.Open(".")
	if err != nil {
		c.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		c.Fatal(err)
	}
	// On POSIX platforms, PWD represents “an absolute pathname of the
	// current working directory.” Since we are changing the working
	// directory, we should also set or update PWD to reflect that.
	switch runtime.GOOS {
	case "windows", "plan9":
		// Windows and Plan 9 do not use the PWD variable.
	default:
		if !filepath.IsAbs(dir) {
			dir, err = os.Getwd()
			if err != nil {
				c.Fatal(err)
			}
		}
		c.Setenv("PWD", dir)
	}
	c.Cleanup(func() {
		err := oldwd.Chdir()
		oldwd.Close()
		if err != nil {
			// It's not safe to continue with tests if we can't
			// get back to the original working directory. Since
			// we are holding a dirfd, this is highly unlikely.
			panic("testing.Chdir: " + err.Error())
		}
	})
}
