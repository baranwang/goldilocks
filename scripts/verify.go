// Verify GoReleaser assets and native launcher behavior. Run from the repository root.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type target struct{ OS, Arch, Dir, Name string }

var targets = []target{
	{"darwin", "arm64", "Darwin-arm64", "goldilocks"},
	{"darwin", "amd64", "Darwin-x86_64", "goldilocks"},
	{"linux", "arm64", "Linux-aarch64", "goldilocks"},
	{"linux", "amd64", "Linux-x86_64", "goldilocks"},
	{"windows", "arm64", "Windows-ARM64", "goldilocks.exe"},
	{"windows", "amd64", "Windows-AMD64", "goldilocks.exe"},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "goldilocks verify:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) == 3 && os.Args[1] == "--binary" {
		return checkBinary(os.Args[2])
	}
	if len(os.Args) != 1 {
		return errors.New("usage: go run ./scripts/verify.go [--binary PATH]")
	}
	raw, err := os.ReadFile(".codex-plugin/plugin.json")
	if err != nil {
		return err
	}
	var manifest struct{ Version string }
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	version := manifest.Version
	if version == "" || strings.ContainsAny(version, " \t\r\n") {
		return errors.New("invalid manifest version")
	}
	if err := check(); err != nil {
		return err
	}
	for _, t := range targets {
		if t.OS == runtime.GOOS && t.Arch == runtime.GOARCH {
			return smoke(t, version)
		}
	}
	fmt.Printf("native smoke: not_verified (%s/%s is not bundled)\n", runtime.GOOS, runtime.GOARCH)
	return nil
}

// Used by GoReleaser's release-only post-build hook before publication.
func checkBinary(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pinned, err := os.ReadFile("scripts/SHA256SUMS")
	if err != nil {
		return err
	}
	want := fmt.Sprintf("%x  %s", sha256.Sum256(data), filepath.Base(path))
	for _, line := range strings.Split(string(pinned), "\n") {
		if line == want {
			return nil
		}
	}
	return fmt.Errorf("pinned SHA256SUMS mismatch: %s", path)
}

func check() error {
	var sums []string
	for _, t := range targets {
		path := filepath.Join("dist", "goldilocks-"+t.Dir+strings.TrimPrefix(t.Name, "goldilocks"))
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		magic := []byte("\x7fELF")
		switch t.OS {
		case "windows":
			magic = []byte("MZ")
		case "darwin":
			magic = []byte{0xcf, 0xfa, 0xed, 0xfe}
		}
		if !bytes.HasPrefix(data, magic) {
			return fmt.Errorf("invalid %s executable format: %s", t.OS, path)
		}
		digest := sha256.Sum256(data)
		sums = append(sums, fmt.Sprintf("%x  %s", digest, filepath.Base(path)))
	}
	sort.Strings(sums)
	actual, err := os.ReadFile("scripts/SHA256SUMS")
	if err != nil {
		return err
	}
	pinned := strings.FieldsFunc(string(actual), func(r rune) bool { return r == '\n' || r == '\r' })
	sort.Strings(pinned)
	if strings.Join(pinned, "\n") != strings.Join(sums, "\n") {
		return errors.New("scripts/SHA256SUMS mismatch")
	}
	generated, err := os.ReadFile("dist/SHA256SUMS")
	if err != nil {
		return err
	}
	entries := strings.FieldsFunc(string(generated), func(r rune) bool { return r == '\n' || r == '\r' })
	sort.Strings(entries)
	if strings.Join(entries, "\n") != strings.Join(sums, "\n") {
		return errors.New("GoReleaser SHA256SUMS mismatch")
	}
	fmt.Println("verified six release executable formats and SHA256SUMS")
	return nil
}

func checkUnrelatedSmoke(event string, output []byte, controllerRoot string) error {
	if len(bytes.TrimSpace(output)) != 0 {
		return fmt.Errorf("unrelated %s was affected", event)
	}
	if _, err := os.Stat(controllerRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unrelated %s mutated isolated controller state", event)
}

func smoke(t target, version string) error {
	root, err := os.MkdirTemp("", "goldilocks plugin root ")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	// Resolve macOS /var aliases so payload cwd matches the actual working directory.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	dataDir := filepath.Join(root, "data")
	binary := filepath.Join(dataDir, "bin", version, t.Dir, t.Name)
	asset := filepath.Join("dist", "goldilocks-"+t.Dir+strings.TrimPrefix(t.Name, "goldilocks"))
	for _, path := range []string{asset, "scripts/goldilocks.sh", "scripts/goldilocks.ps1", "scripts/SHA256SUMS", ".codex-plugin/plugin.json", filepath.Join("skills", "model-routing", "SKILL.md")} {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		destination := filepath.Join(root, path)
		if path == asset {
			destination = binary
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(destination, data, 0755); err != nil {
			return err
		}
	}
	cwd := filepath.Join(root, "other working directory")
	if err := os.Mkdir(cwd, 0755); err != nil {
		return err
	}
	versionCommand := exec.Command(binary, "--version")
	versionCommand.Dir = cwd
	output, err := versionCommand.Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(output)) != version {
		return errors.New("release version mismatch")
	}
	raw, err := os.ReadFile("hooks/hooks.json")
	if err != nil {
		return err
	}
	var config struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command, CommandWindows string }
		}
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return err
	}
	codexHome := filepath.Join(root, "codex-home")
	controllerRoot := filepath.Join(codexHome, "goldilocks", "pr-watch")
	for _, event := range []string{"SessionStart", "SubagentStart", "PostToolUse", "SubagentStop", "Stop"} {
		groups := config.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			return fmt.Errorf("expected one command handler for %s", event)
		}
		handler := groups[0].Hooks[0]
		command := handler.Command
		launcher := []string{"/bin/sh", "-c", command}
		if runtime.GOOS == "windows" {
			command = handler.CommandWindows
			launcher = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command}
		}
		if command == "" {
			return fmt.Errorf("missing native command for %s", event)
		}
		payload, _ := json.Marshal(map[string]string{"hook_event_name": event, "cwd": cwd,
			"session_id": "00000000-0000-4000-8000-000000000001", "agent_id": "00000000-0000-4000-8000-000000000002",
			"tool_name": "mcp__codex_app__read_thread"})
		cmd := exec.Command(launcher[0], launcher[1:]...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "PLUGIN_ROOT="+root, "PLUGIN_DATA="+dataDir, "CODEX_HOME="+codexHome)
		cmd.Stdin = bytes.NewReader(payload)
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("%s smoke: %w", event, err)
		}
		routing := event == "SessionStart" || event == "SubagentStart"
		if !routing {
			if err := checkUnrelatedSmoke(event, output, controllerRoot); err != nil {
				return err
			}
		} else {
			var result struct {
				HookSpecificOutput struct{ HookEventName, AdditionalContext string }
			}
			if err := json.Unmarshal(output, &result); err != nil {
				return err
			}
			if result.HookSpecificOutput.HookEventName != event || !strings.Contains(result.HookSpecificOutput.AdditionalContext, "## Hard boundary") {
				return errors.New("routing injection failed")
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, "scripts/SHA256SUMS"), []byte("invalid\n"), 0644); err != nil {
		return err
	}
	command := config.Hooks["SessionStart"][0].Hooks[0]
	launcher := []string{"/bin/sh", "-c", command.Command}
	if runtime.GOOS == "windows" {
		launcher = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command.CommandWindows}
	}
	cmd := exec.Command(launcher[0], launcher[1:]...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PLUGIN_ROOT="+root, "PLUGIN_DATA="+dataDir, "CODEX_HOME="+codexHome)
	output, err = cmd.CombinedOutput()
	if err == nil || len(bytes.TrimSpace(output)) == 0 {
		return errors.New("invalid pinned checksum must fail with a diagnostic")
	}
	fmt.Printf("native smoke verified %s/%s: version %s, five hook events, invalid-checksum diagnostic\n", t.OS, t.Arch, version)
	return nil
}
