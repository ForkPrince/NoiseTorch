// This file is part of the program "NoiseTorch-ng".
// Please see the LICENSE file for copyright information.

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindInstalledLadspaPlugin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ladspaPluginFile), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	os.Setenv("LADSPA_PATH", dir)
	defer os.Unsetenv("LADSPA_PATH")

	got := findInstalledLadspaPlugin()
	want := filepath.Join(dir, ladspaPluginFile)
	if got != want {
		t.Fatalf("findInstalledLadspaPlugin() = %q, want %q", got, want)
	}
}

func TestFindInstalledLadspaPluginNotInstalled(t *testing.T) {
	os.Setenv("LADSPA_PATH", t.TempDir())
	old := getPipeWirePulseLadspaPath
	getPipeWirePulseLadspaPath = func() string { return "" }
	defer func() {
		os.Unsetenv("LADSPA_PATH")
		getPipeWirePulseLadspaPath = old
	}()

	if got := findInstalledLadspaPlugin(); got != "" {
		t.Fatalf("expected no plugin, got %q", got)
	}
}

func TestConfigurePipeWirePulseLadspa(t *testing.T) {
	cfg := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", cfg)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	userDir := filepath.Join(t.TempDir(), "ladspa")
	configurePipeWirePulseLadspa(userDir)

	dropin := filepath.Join(cfg, "systemd", "user", "pipewire-pulse.service.d", "10-noisetorch-ladspa.conf")
	b, err := os.ReadFile(dropin)
	if err != nil {
		t.Fatalf("expected drop-in file to be written: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "Environment=LADSPA_PATH=") {
		t.Fatalf("drop-in missing LADSPA_PATH line:\n%s", content)
	}
	if !strings.Contains(content, userDir) {
		t.Fatalf("drop-in missing userLadspaDir %q:\n%s", userDir, content)
	}
	// The default dirs must be preserved so other plugins still resolve.
	for _, d := range defaultLadspaDirs {
		if !strings.Contains(content, d) {
			t.Fatalf("drop-in missing default dir %q:\n%s", d, content)
		}
	}
}

func TestEnsurePipeWirePluginInstallsOnDemand(t *testing.T) {
	home := t.TempDir()
	cfg := t.TempDir()
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", cfg)
	os.Setenv("LADSPA_PATH", "")

	oldPath := getPipeWirePulseLadspaPath
	getPipeWirePulseLadspaPath = func() string { return "" }
	restarts := 0
	oldRestart := restartPipeWirePulseForLadspa
	restartPipeWirePulseForLadspa = func(ctx *ntcontext) error { restarts++; return nil }
	defer func() {
		os.Unsetenv("HOME")
		os.Unsetenv("XDG_CONFIG_HOME")
		os.Unsetenv("LADSPA_PATH")
		getPipeWirePulseLadspaPath = oldPath
		restartPipeWirePulseForLadspa = oldRestart
	}()

	ctx := &ntcontext{serverInfo: audioserverinfo{servertype: servertype_pipewire, major: 1, minor: 6, patch: 8}}
	if err := ensurePipeWirePlugin(ctx); err != nil {
		t.Fatalf("ensurePipeWirePlugin: %v", err)
	}

	installed := filepath.Join(home, ".local", "lib", "ladspa", ladspaPluginFile)
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("expected plugin installed to %s: %v", installed, err)
	}
	if restarts != 1 {
		t.Fatalf("expected pipewire-pulse restart once, got %d", restarts)
	}
	if !ctx.pipeWirePluginReady {
		t.Fatal("expected pipeWirePluginReady to be set")
	}

	// Second call must be a no-op.
	if err := ensurePipeWirePlugin(ctx); err != nil {
		t.Fatalf("ensurePipeWirePlugin second call: %v", err)
	}
	if restarts != 1 {
		t.Fatalf("expected no additional restart, got %d", restarts)
	}
}

func TestEnsurePipeWirePluginPrefersDaemonDir(t *testing.T) {
	home := t.TempDir()
	cfg := t.TempDir()
	daemonDir := t.TempDir()
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", cfg)
	os.Setenv("LADSPA_PATH", "")

	oldPath := getPipeWirePulseLadspaPath
	getPipeWirePulseLadspaPath = func() string { return daemonDir }
	restarts := 0
	oldRestart := restartPipeWirePulseForLadspa
	restartPipeWirePulseForLadspa = func(ctx *ntcontext) error { restarts++; return nil }
	defer func() {
		os.Unsetenv("HOME")
		os.Unsetenv("XDG_CONFIG_HOME")
		os.Unsetenv("LADSPA_PATH")
		getPipeWirePulseLadspaPath = oldPath
		restartPipeWirePulseForLadspa = oldRestart
	}()

	ctx := &ntcontext{serverInfo: audioserverinfo{servertype: servertype_pipewire, major: 1, minor: 6, patch: 8}}
	if err := ensurePipeWirePlugin(ctx); err != nil {
		t.Fatalf("ensurePipeWirePlugin: %v", err)
	}

	// installed into the daemon-searched dir, so no restart must happen
	if _, err := os.Stat(filepath.Join(daemonDir, ladspaPluginFile)); err != nil {
		t.Fatalf("expected plugin installed to %s: %v", daemonDir, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "lib", "ladspa", ladspaPluginFile)); !os.IsNotExist(err) {
		t.Fatalf("plugin should not have been installed to the user dir")
	}
	if restarts != 0 {
		t.Fatalf("expected no restart when installing into a daemon-searched dir, got %d", restarts)
	}
}

func TestEnsurePipeWirePluginOldPipeWireUsesTempPath(t *testing.T) {
	home := t.TempDir()
	os.Setenv("HOME", home)
	os.Setenv("LADSPA_PATH", "")
	oldPath := getPipeWirePulseLadspaPath
	getPipeWirePulseLadspaPath = func() string { return "" }
	defer func() {
		os.Unsetenv("HOME")
		os.Unsetenv("LADSPA_PATH")
		getPipeWirePulseLadspaPath = oldPath
	}()

	ctx := &ntcontext{
		serverInfo: audioserverinfo{servertype: servertype_pipewire, major: 1, minor: 2, patch: 0},
		librnnoise: "/tmp/librnnoise-test.so",
	}
	if err := ensurePipeWirePlugin(ctx); err != nil {
		t.Fatalf("ensurePipeWirePlugin: %v", err)
	}
	if got := pipeWirePluginArg(ctx); got != "/tmp/librnnoise-test.so" {
		t.Fatalf("pipeWirePluginArg() = %q, want the temp .so path for old PipeWire", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "lib", "ladspa", ladspaPluginFile)); !os.IsNotExist(err) {
		t.Fatal("old PipeWire must not trigger a plugin install")
	}
}

func TestEnsurePipeWirePluginRestartFailurePropagates(t *testing.T) {
	home := t.TempDir()
	cfg := t.TempDir()
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", cfg)
	os.Setenv("LADSPA_PATH", "")

	oldPath := getPipeWirePulseLadspaPath
	getPipeWirePulseLadspaPath = func() string { return "" }
	oldRestart := restartPipeWirePulseForLadspa
	restartPipeWirePulseForLadspa = func(ctx *ntcontext) error { return errStub }
	defer func() {
		os.Unsetenv("HOME")
		os.Unsetenv("XDG_CONFIG_HOME")
		os.Unsetenv("LADSPA_PATH")
		getPipeWirePulseLadspaPath = oldPath
		restartPipeWirePulseForLadspa = oldRestart
	}()

	ctx := &ntcontext{serverInfo: audioserverinfo{servertype: servertype_pipewire, major: 1, minor: 6, patch: 8}}
	if err := ensurePipeWirePlugin(ctx); err == nil {
		t.Fatal("expected error when the pipewire-pulse restart fails")
	}
	if ctx.pipeWirePluginReady {
		t.Fatal("pipeWirePluginReady must not be set when ensure failed")
	}
}

func TestPipeWireAtLeast(t *testing.T) {
	cases := []struct {
		si   audioserverinfo
		want bool
	}{
		{audioserverinfo{servertype: servertype_pipewire, major: 1, minor: 6, patch: 3}, true},
		{audioserverinfo{servertype: servertype_pipewire, major: 1, minor: 6, patch: 8}, true},
		{audioserverinfo{servertype: servertype_pipewire, major: 2, minor: 0, patch: 0}, true},
		{audioserverinfo{servertype: servertype_pipewire, major: 1, minor: 6, patch: 2}, false},
		{audioserverinfo{servertype: servertype_pipewire, major: 1, minor: 5, patch: 99}, false},
		{audioserverinfo{servertype: servertype_pipewire, major: 0, minor: 3, patch: 40}, false},
		{audioserverinfo{servertype: servertype_pipewire}, true}, // unknown version, assume recent
		{audioserverinfo{servertype: servertype_pulse, major: 16, minor: 0, patch: 0}, false},
	}
	for _, c := range cases {
		ctx := &ntcontext{serverInfo: c.si}
		if got := pipeWireAtLeast(ctx, 1, 6, 3); got != c.want {
			t.Errorf("pipeWireAtLeast(%+v) = %v, want %v", c.si, got, c.want)
		}
	}
}

var errStub = errors.New("stub restart failure")
