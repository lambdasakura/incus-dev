package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunSuccess(t *testing.T) {
	var stderr bytes.Buffer

	if code := run([]string{"--version"}, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0 (%s)", code, stderr.String())
	}
}

func TestRunReportsError(t *testing.T) {
	var stderr bytes.Buffer

	code := run([]string{"validate", "-C", t.TempDir()}, &stderr)

	if code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "[idev] error:") {
		t.Errorf("stderr = %q, want the error to be reported", stderr.String())
	}
}

func TestRunValidatesProject(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".incus-dev", "dev.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "schema: 1\nproject:\n  name: main-test\ninstance:\n  image: images:ubuntu/24.04\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// With no Incus reachable: validate must not need one at all
	// (spec 04-cli.md 4.7).
	t.Setenv("INCUS_SOCKET", filepath.Join(t.TempDir(), "does-not-exist.socket"))

	var stderr bytes.Buffer
	args := []string{"validate", "-C", root}
	if code := run(args, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0 (%s)", code, stderr.String())
	}
}

func TestMainUsesRunExitCode(t *testing.T) {
	original := osExit
	defer func() { osExit = original }()

	var got int
	osExit = func(code int) { got = code }

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"idev", "validate", "-C", t.TempDir()}

	main()

	if got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
}

// A second Ctrl-C has to reach the default handler.
//
// signal.NotifyContext keeps the handler installed until stop(), but the
// goroutine behind it delivers only the first signal; every later one lands in
// a channel nobody reads. Several Client methods bind the context and discard
// it -- the library takes none, and its WithContext mutates the shared client
// rather than returning a copy -- so a daemon that answers the handshake and
// then stops answering leaves idev in a call the first interrupt cannot reach.
// Without the guard the only way out is SIGKILL, which skips every defer and
// can leave the terminal in raw mode after idev shell.
func TestASecondSignalStillKillsIt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("unix sockets and signals, so linux only")
	}

	dir := t.TempDir()
	socket := filepath.Join(dir, "incus.socket")
	serveWedgedDaemon(t, socket)

	root := filepath.Join(dir, "project")
	writeProject(t, root, "signal-test", "")

	cmd := exec.Command(buildIdev(t, dir), "status", "-C", root)
	cmd.Env = append(os.Environ(), "INCUS_SOCKET="+socket)

	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// It has to be wedged past the handshake, or the signals below prove
	// nothing: an idev that exited on its own passes whatever the handling
	// does. This is the failure the first version of this test had.
	time.Sleep(2 * time.Second)
	select {
	case err := <-done:
		t.Fatalf("idev exited on its own (%v); the daemon did not wedge it:\n%s", err, output.String())
	default:
	}

	for range 2 {
		_ = cmd.Process.Signal(os.Interrupt)
		time.Sleep(500 * time.Millisecond)
	}

	select {
	case err := <-done:
		// 128+SIGINT, what the shell reports for an interrupted process --
		// and what says the second signal ended it rather than the command
		// finishing.
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 130 {
			t.Errorf("idev exited with %v, want exit status 130 from the second interrupt", err)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("idev survived two interrupts: the user cannot stop it")
	}
}

// Connecting to a daemon that never answers is interruptible on the first
// signal, because nothing has been changed at that point and Connect gives up
// with its context.
func TestConnectingIsInterruptible(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("unix sockets and signals, so linux only")
	}

	dir := t.TempDir()
	socket := filepath.Join(dir, "incus.socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	root := filepath.Join(dir, "project")
	writeProject(t, root, "connect-test", "")

	cmd := exec.Command(buildIdev(t, dir), "status", "-C", root)
	cmd.Env = append(os.Environ(), "INCUS_SOCKET="+socket)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	time.Sleep(time.Second)
	select {
	case err := <-done:
		t.Fatalf("idev exited on its own (%v); the socket did not wedge it", err)
	default:
	}

	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("one interrupt did not stop idev while it was still connecting")
	}
}

// serveWedgedDaemon answers the client's handshake and nothing else, which is
// what leaves idev inside a Client call rather than in Connect.
func serveWedgedDaemon(t *testing.T, socket string) {
	t.Helper()

	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"sync","status":"Success","status_code":200,`+
			`"metadata":{"api_extensions":[],"api_status":"stable","api_version":"1.0",`+
			`"auth":"trusted","public":false,`+
			`"environment":{"server":"incus","server_version":"6.0"},"config":{}}}`)
	})
	mux.HandleFunc("/", func(_ http.ResponseWriter, _ *http.Request) { <-stop })

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

// buildIdev builds the binary under test.
func buildIdev(t *testing.T, dir string) string {
	t.Helper()

	bin := filepath.Join(dir, "idev")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// writeProject writes a minimal dev.yml, plus extra lines when given.
func writeProject(t *testing.T, root, name, extra string) {
	t.Helper()

	config := filepath.Join(root, ".incus-dev", "dev.yml")
	if err := os.MkdirAll(filepath.Dir(config), 0o750); err != nil {
		t.Fatal(err)
	}
	body := "schema: 1\nproject:\n  name: " + name + "\n" + extra +
		"instance:\n  image: images:debian/12\n"
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// One interrupt is enough for a command that runs no Incus call.
//
// buildApp spawns git to resolve project.scope: branch, and was handing it
// context.Background(), so exec.CommandContext had nothing to cancel and the
// git child outlived idev. The second-signal guard above is the backstop; this
// is the first Ctrl-C working as it should.
func TestAnOfflineCommandStopsOnTheFirstSignal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("signals and a shell script, so linux only")
	}

	dir := t.TempDir()
	// A git that never answers, ahead of the real one on PATH.
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "git"),
		[]byte("#!/bin/sh\nsleep 300\n"), 0o700); err != nil { //nolint:gosec // a stub on a test PATH
		t.Fatal(err)
	}

	root := filepath.Join(dir, "project")
	writeProject(t, root, "branch-test", "  scope: branch\n")

	bin := buildIdev(t, dir)

	// validate, which makes no Incus call at all: the block is git alone.
	cmd := exec.Command(bin, "validate", "-C", root)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"),
		"INCUS_SOCKET="+filepath.Join(dir, "no.socket"))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	time.Sleep(time.Second)
	select {
	case err := <-done:
		t.Fatalf("idev exited on its own (%v); git did not block it:\n%s", err, output.String())
	default:
	}

	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("one interrupt did not stop an offline command")
	}
}

// The watcher itself, in process. The subprocess tests above prove the
// behaviour end to end but contribute no coverage, and each branch here is a
// way for a Ctrl-C to go missing.
func TestExitOnNextSignal(t *testing.T) {
	// absorb keeps SIGUSR1 from ending the test binary, and proves the signal
	// really was delivered. Without it a subtest that expects nothing to
	// happen would kill the run instead of failing.
	absorb := func(t *testing.T) <-chan os.Signal {
		t.Helper()
		received := make(chan os.Signal, 1)
		signal.Notify(received, syscall.SIGUSR1)
		t.Cleanup(func() { signal.Stop(received) })
		return received
	}
	raise := func(t *testing.T, delivered <-chan os.Signal) {
		t.Helper()
		if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
			t.Skipf("cannot signal self: %v", err)
		}
		select {
		case <-delivered:
		case <-time.After(5 * time.Second):
			t.Fatal("the signal was never delivered")
		}
	}
	watch := func(t *testing.T, ctx context.Context) <-chan int {
		t.Helper()
		exited := make(chan int, 1)
		swapExit(t)
		osExit = func(code int) { exited <- code }
		t.Cleanup(exitOnNextSignal(ctx, syscall.SIGUSR1))
		return exited
	}

	t.Run("a signal before cancellation is not this watcher's", func(t *testing.T) {
		delivered := absorb(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		exited := watch(t, ctx)

		// NotifyContext owns the first signal; arming for it here would end
		// the process on the Ctrl-C that is supposed to cancel politely.
		raise(t, delivered)

		select {
		case code := <-exited:
			t.Errorf("exited with %d on the first signal, want it left alone", code)
		case <-time.After(300 * time.Millisecond):
		}
	})

	t.Run("a signal after cancellation ends the process", func(t *testing.T) {
		delivered := absorb(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		exited := watch(t, ctx)

		cancel()
		time.Sleep(200 * time.Millisecond) // let it arm
		raise(t, delivered)

		select {
		case code := <-exited:
			if want := 128 + int(syscall.SIGUSR1); code != want {
				t.Errorf("exit code = %d, want %d", code, want)
			}
		case <-time.After(5 * time.Second):
			t.Error("the second signal did not end the process")
		}
	})

	t.Run("released after cancellation, before a second signal", func(t *testing.T) {
		delivered := absorb(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		swapExit(t)
		exited := make(chan int, 1)
		osExit = func(code int) { exited <- code }

		release := exitOnNextSignal(ctx, syscall.SIGUSR1)
		cancel()
		time.Sleep(200 * time.Millisecond) // let it arm
		release()
		time.Sleep(200 * time.Millisecond)
		raise(t, delivered)

		select {
		case code := <-exited:
			t.Errorf("exited with %d after being released", code)
		case <-time.After(300 * time.Millisecond):
		}
	})
}

// swapExit makes osExit a no-op for the duration of a test and returns the
// restore, so a stray signal cannot end the test binary.
func swapExit(t *testing.T) func() {
	t.Helper()

	original := osExit
	osExit = func(int) {}
	restore := func() { osExit = original }
	t.Cleanup(restore)
	return restore
}
