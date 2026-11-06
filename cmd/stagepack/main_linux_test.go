package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/export"
)

const signalHelperEnv = "STAGEPACK_TEST_EXPORT_SIGNAL_HELPER"

// TestExportPptxSignalHelper is not a test of its own: TestExportPptxStopsOnSignal runs the
// test binary with it as `export-pptx --headless`, with an export that waits for its context.
func TestExportPptxSignalHelper(t *testing.T) {
	if os.Getenv(signalHelperEnv) != "1" {
		return
	}
	exportDeck = func(ctx context.Context, o export.Options) (export.Result, error) {
		os.Stdout.WriteString("exporting\n")
		select {
		case <-ctx.Done():
			return export.Result{}, export.ErrCancelled
		case <-time.After(30 * time.Second):
			return export.Result{}, nil
		}
	}
	os.Exit(run([]string{"export-pptx", "--headless", "--home", "/h", "x" + brand.Ext}, os.Stdout, os.Stderr))
}

// Ctrl-C or SIGTERM cancels an export, as its box's Cancel does, so that the exporter is
// stopped and the work folder and lock are removed; it does not kill the program outright.
func TestExportPptxStopsOnSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestExportPptxSignalHelper$")
			cmd.Env = append(os.Environ(), signalHelperEnv+"=1")
			var stderr strings.Builder
			cmd.Stderr = &stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			sc := bufio.NewScanner(stdout)
			for sc.Scan() && sc.Text() != "exporting" {
			}
			if sc.Text() != "exporting" {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatalf("the export did not start; stderr: %s", stderr.String())
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 3 {
				t.Fatalf("after %v: %v, want exit 3 (cancelled); stderr: %s", sig, err, stderr.String())
			}
			if !strings.Contains(stderr.String(), export.ErrCancelled.Error()) {
				t.Errorf("stderr %q does not say the export was cancelled", stderr.String())
			}
		})
	}
}
