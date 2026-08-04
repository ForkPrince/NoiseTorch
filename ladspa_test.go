// This file is part of the program "NoiseTorch-ng".
// Please see the LICENSE file for copyright information.

package main

import (
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
	defer os.Unsetenv("LADSPA_PATH")

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
	defer func() {
		os.Unsetenv("HOME")
		os.Unsetenv("XDG_CONFIG_HOME")
		os.Unsetenv("LADSPA_PATH")
	}()

	ctx := &ntcontext{}
	ensurePipeWirePlugin(ctx)

	installed := filepath.Join(home, ".local", "lib", "ladspa", ladspaPluginFile)
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("expected plugin installed to %s: %v", installed, err)
	}

	// Second call must not do anything more.
	ctx.pipeWirePluginReady = false
	ensurePipeWirePlugin(ctx)
}