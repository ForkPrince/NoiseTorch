// This file is part of the program "NoiseTorch-ng".
// Please see the LICENSE file for copyright information.

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/noisetorch/pulseaudio"
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

// daemonLadspaSearchDirs returns the directories the running pipewire-pulse
// daemon actually searches: its built-in defaults plus its own LADSPA_PATH.
func daemonLadspaSearchDirs() []string {
	dirs := make([]string, 0, len(defaultLadspaDirs)+3)
	dirs = append(dirs, defaultLadspaDirs...)
	for _, d := range filepath.SplitList(getPipeWirePulseLadspaPath()) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// dirInDaemonSearchPath reports whether the daemon already searches dir.
func dirInDaemonSearchPath(dir string) bool {
	for _, d := range daemonLadspaSearchDirs() {
		if d == dir {
			return true
		}
	}
	return false
}

// userLadspaDir returns a user-writable LADSPA directory (~/.local/lib/ladspa).
func userLadspaDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "lib", "ladspa"), nil
}

// getPipeWirePulseLadspaPath is indirection over pipeWirePulseLadspaPath so
// tests can stub out the running daemon.
var getPipeWirePulseLadspaPath = pipeWirePulseLadspaPath

// findInstalledLadspaPlugin returns the path of rnnoise_ladspa.so if it already
// sits in a directory that will be searched, otherwise it returns "".
func findInstalledLadspaPlugin() string {
	return findInstalledLadspaPluginIn(append(ladspaSearchDirs(),
		filepath.SplitList(getPipeWirePulseLadspaPath())...))
}

func findInstalledLadspaPluginIn(dirs []string) string {
	seen := make(map[string]bool)
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
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

// pipeWireAtLeast reports whether the running PipeWire server is at least
// major.minor.patch. An unparseable version is treated as recent.
func pipeWireAtLeast(ctx *ntcontext, major, minor, patch int) bool {
	si := ctx.serverInfo
	if si.servertype != servertype_pipewire {
		return false
	}
	if si.major == 0 && si.minor == 0 {
		return true
	}
	if si.major != major {
		return si.major > major
	}
	if si.minor != minor {
		return si.minor > minor
	}
	return si.patch >= patch
}

// pipeWirePluginArg returns the plugin= argument for module-ladspa-source/sink.
// PipeWire >= 1.6.3 only resolves plugins by name inside a LADSPA_PATH
// directory of the daemon; older versions also accept an absolute path such
// as the /tmp file NoiseTorch dumps.
func pipeWirePluginArg(ctx *ntcontext) string {
	if pipeWireAtLeast(ctx, 1, 6, 3) {
		return ladspaPluginName
	}
	return ctx.librnnoise
}

var pipeWireEnsureMu sync.Mutex

// restartPipeWirePulseForLadspa restarts the pipewire-pulse daemon so a new
// systemd drop-in takes effect, then waits until the audio server accepts
// connections again. It is a variable so tests can stub it out.
var restartPipeWirePulseForLadspa = func(ctx *ntcontext) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found: add the LADSPA directory to the LADSPA_PATH of pipewire-pulse manually and restart it")
	}
	old := ctx.paClient
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "--user", "restart", "pipewire-pulse").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user restart pipewire-pulse failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	log.Printf("Restarted pipewire-pulse, waiting for the audio server to come back\n")
	return waitForAudioServerReconnect(ctx, old, 30*time.Second)
}

// waitForAudioServerReconnect blocks until ctx.paClient is a connected client
// other than old (in the UI the connection watchdog replaces it; in CLI mode
// we create a new client ourselves), or the timeout expires.
func waitForAudioServerReconnect(ctx *ntcontext, old *pulseaudio.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c := ctx.paClient
		if c != nil && c != old && c.Connected() {
			return nil
		}
		if c == nil || c == old {
			if nc, err := pulseaudio.NewClient(); err == nil && nc.Connected() {
				ctx.paClient = nc
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("audio server did not come back within %s", timeout)
}

// installPipeWirePlugin copies the bundled rnnoise_ladspa.so into the first
// user-writable candidate directory and returns that directory. Directories
// already in the daemon's LADSPA_PATH are preferred, so that no daemon
// restart is needed (the path is scanned on every plugin load).
func installPipeWirePlugin() (string, error) {
	var candidates []string
	for _, d := range filepath.SplitList(getPipeWirePulseLadspaPath()) {
		if d != "" {
			candidates = append(candidates, d)
		}
	}
	if ud, err := userLadspaDir(); err == nil {
		candidates = append(candidates, ud)
	} else {
		log.Printf("Could not determine user LADSPA dir: %v", err)
	}
	var lastErr error
	for _, d := range candidates {
		if err := os.MkdirAll(d, 0o755); err != nil {
			lastErr = err
			continue
		}
		if err := os.WriteFile(filepath.Join(d, ladspaPluginFile), libRNNoise, 0o755); err != nil {
			lastErr = err
			continue
		}
		return d, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate LADSPA directories")
	}
	return "", lastErr
}

// ensurePipeWirePlugin makes sure PipeWire >= 1.6.3 can resolve the rnnoise
// LADSPA plugin by name.
//
// Starting with PipeWire 1.6.3 the filter-graph LADSPA backend only loads
// plugins whose full path is inside a LADSPA_PATH directory of the daemon;
// it no longer accepts an arbitrary absolute path such as the /tmp file
// NoiseTorch dumps. This is the fallback chain used to satisfy that:
//
//  1. If rnnoise_ladspa.so is already installed in a directory the running
//     daemon searches, use it as-is.
//  2. Otherwise copy the bundled plugin into a user-writable directory that
//     is already in the daemon's LADSPA_PATH, if one exists (no restart
//     needed).
//  3. Otherwise copy it into ~/.local/lib/ladspa, extend the daemon's
//     LADSPA_PATH through a systemd user drop-in, and restart pipewire-pulse
//     once so the change takes effect.
//
// Any failure is returned so the caller can surface it instead of failing
// silently.
func ensurePipeWirePlugin(ctx *ntcontext) error {
	pipeWireEnsureMu.Lock()
	defer pipeWireEnsureMu.Unlock()

	if ctx.pipeWirePluginReady {
		return nil
	}
	if ctx.serverInfo.servertype != servertype_pipewire {
		return nil
	}
	if !pipeWireAtLeast(ctx, 1, 6, 3) {
		// old PipeWire accepts the dumped /tmp plugin path directly
		return nil
	}

	if findInstalledLadspaPluginIn(daemonLadspaSearchDirs()) != "" {
		log.Printf("%s already available in a LADSPA directory", ladspaPluginFile)
		ctx.pipeWirePluginReady = true
		return nil
	}

	dir, err := installPipeWirePlugin()
	if err != nil {
		return fmt.Errorf("could not install %s into a user-writable LADSPA directory: %w", ladspaPluginFile, err)
	}
	log.Printf("Installed %s to %s", ladspaPluginFile, dir)

	if dirInDaemonSearchPath(dir) {
		ctx.pipeWirePluginReady = true
		return nil
	}

	if err := configurePipeWirePulseLadspa(dir); err != nil {
		return err
	}
	if err := restartPipeWirePulseForLadspa(ctx); err != nil {
		return err
	}
	ctx.pipeWirePluginReady = true
	return nil
}

// configurePipeWirePulseLadspa writes a systemd user drop-in that adds userDir
// to the LADSPA_PATH of the pipewire-pulse service. Any LADSPA_PATH the running
// daemon already has is preserved.
func configurePipeWirePulseLadspa(userDir string) error {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("could not determine user config dir: %w", err)
	}

	serviceDir := filepath.Join(configHome, "systemd", "user", "pipewire-pulse.service.d")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		return fmt.Errorf("could not create systemd drop-in dir %s: %w", serviceDir, err)
	}

	paths := []string{userDir}
	for _, d := range filepath.SplitList(getPipeWirePulseLadspaPath()) {
		if d != "" && d != userDir {
			paths = append(paths, d)
		}
	}
	paths = append(paths, defaultLadspaDirs...)

	dropin := filepath.Join(serviceDir, "10-noisetorch-ladspa.conf")
	content := "[Service]\nEnvironment=LADSPA_PATH=" + strings.Join(paths, ":") + "\n"
	if err := os.WriteFile(dropin, []byte(content), 0o644); err != nil {
		return fmt.Errorf("could not write systemd drop-in %s: %w", dropin, err)
	}
	log.Printf("Wrote pipewire-pulse LADSPA_PATH drop-in to %s", dropin)
	return nil
}
