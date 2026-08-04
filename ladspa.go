// This file is part of the program "NoiseTorch-ng".
// Please see the LICENSE file for copyright information.

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const ladspaPluginName = "rnnoise_ladspa"
const ladspaPluginFile = "rnnoise_ladspa.so"

// defaultLadspaDirs are the directories PipeWire's filter-graph backend probes
// for LADSPA plugins when no LADSPA_PATH is set in the daemon's environment.
var defaultLadspaDirs = []string{
	"/usr/lib64/ladspa",
	"/usr/lib/ladspa",
	"/usr/lib",
	"/usr/local/lib64/ladspa",
	"/usr/local/lib/ladspa",
}

// ladspaSearchDirs returns every directory that could hold a plugin for
// PipeWire's filter-graph backend: the default dirs plus anything in the
// LADSPA_PATH of this process.
func ladspaSearchDirs() []string {
	dirs := make([]string, 0, len(defaultLadspaDirs)+3)
	dirs = append(dirs, defaultLadspaDirs...)
	for _, d := range filepath.SplitList(os.Getenv("LADSPA_PATH")) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// userLadspaDir returns a user-writable LADSPA directory (~/.local/lib/ladspa).
func userLadspaDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "lib", "ladspa"), nil
}

// findInstalledLadspaPlugin returns the path of rnnoise_ladspa.so if it already
// sits in a directory that PipeWire's filter-graph backend will search,
// otherwise it returns "".
func findInstalledLadspaPlugin() string {
	for _, d := range ladspaSearchDirs() {
		p := filepath.Join(d, ladspaPluginFile)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// pipeWirePulsePid returns the process id of the running pipewire-pulse daemon,
// or 0 if none was found.
func pipeWirePulsePid() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == "pipewire-pulse" {
			pid, _ := strconv.Atoi(e.Name())
			return pid
		}
	}
	return 0
}

// pipeWirePulseLadspaPath returns the LADSPA_PATH from the environment of the
// running pipewire-pulse daemon, or "" if it is unset or the daemon is missing.
func pipeWirePulseLadspaPath() string {
	pid := pipeWirePulsePid()
	if pid == 0 {
		return ""
	}
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return ""
	}
	for _, entry := range strings.Split(string(b), "\x00") {
		if strings.HasPrefix(entry, "LADSPA_PATH=") {
			return strings.TrimPrefix(entry, "LADSPA_PATH=")
		}
	}
	return ""
}

// ensurePipeWirePlugin makes sure PipeWire 1.6.3+ can find rnnoise_ladspa.so.
//
// Starting with PipeWire 1.6.3 the filter-graph LADSPA backend only loads
// plugins whose full path is inside a LADSPA_PATH directory; it no longer
// accepts an arbitrary absolute path such as the /tmp file NoiseTorch used to
// dump. This is the fallback chain used to satisfy that:
//
//  1. If rnnoise_ladspa.so is already installed in a directory PipeWire
//     searches (e.g. /usr/lib/ladspa), use it as-is.
//  2. Otherwise copy the bundled plugin into a user-writable LADSPA directory
//     (~/.local/lib/ladspa) and extend the pipewire-pulse daemon's LADSPA_PATH
//     through a systemd user drop-in so it is picked up.
func ensurePipeWirePlugin(ctx *ntcontext) {
	if ctx.pipeWirePluginReady {
		return
	}
	ctx.pipeWirePluginReady = true

	if findInstalledLadspaPlugin() != "" {
		log.Printf("rnnoise_ladspa.so already available in a LADSPA directory")
		return
	}

	dir, err := userLadspaDir()
	if err != nil {
		log.Printf("Could not determine user LADSPA dir: %v", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("Could not create LADSPA dir %s: %v", dir, err)
		return
	}

	dst := filepath.Join(dir, ladspaPluginFile)
	if err := os.WriteFile(dst, libRNNoise, 0o755); err != nil {
		log.Printf("Could not install %s to %s: %v", ladspaPluginName, dst, err)
		return
	}
	log.Printf("Installed %s to %s", ladspaPluginName, dst)

	configurePipeWirePulseLadspa(dir)
}

// configurePipeWirePulseLadspa writes a systemd user drop-in that adds userDir
// to the LADSPA_PATH of the pipewire-pulse service. Any LADSPA_PATH the running
// daemon already has is preserved.
func configurePipeWirePulseLadspa(userDir string) {
	configHome, err := os.UserConfigDir()
	if err != nil {
		log.Printf("Could not determine user config dir: %v", err)
		return
	}

	serviceDir := filepath.Join(configHome, "systemd", "user", "pipewire-pulse.service.d")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		log.Printf("Could not create systemd drop-in dir %s: %v", serviceDir, err)
		return
	}

	paths := []string{userDir}
	for _, d := range filepath.SplitList(pipeWirePulseLadspaPath()) {
		if d != "" && d != userDir {
			paths = append(paths, d)
		}
	}
	paths = append(paths, defaultLadspaDirs...)

	dropin := filepath.Join(serviceDir, "10-noisetorch-ladspa.conf")
	content := "[Service]\nEnvironment=LADSPA_PATH=" + strings.Join(paths, ":") + "\n"
	if err := os.WriteFile(dropin, []byte(content), 0o644); err != nil {
		log.Printf("Could not write systemd drop-in %s: %v", dropin, err)
		return
	}
	log.Printf("Wrote pipewire-pulse LADSPA_PATH drop-in to %s", dropin)

	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		log.Printf("Could not run 'systemctl --user daemon-reload': %v", err)
	}

	log.Printf("Restart pipewire-pulse ('systemctl --user restart pipewire-pulse') for the LADSPA_PATH change to take effect.")
}