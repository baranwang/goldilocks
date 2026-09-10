// Build and verify the bundled CLI. Run from the repository root.
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
		fmt.Fprintln(os.Stderr, "goldilocks build:", err)
		os.Exit(1)
	}
}

func run() error {
	checkOnly := len(os.Args) == 2 && os.Args[1] == "--check"
	if len(os.Args) != 1 && !checkOnly {
		return errors.New("usage: go run ./scripts/build.go [--check]")
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
	if !checkOnly {
		if err := build(version); err != nil {
			return err
		}
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

func build(version string) error {
	stage, err := os.MkdirTemp("", "goldilocks-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, t := range targets {
		output := filepath.Join(stage, t.Dir, t.Name)
		if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			return err
		}
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -X main.version="+version, "-o", output, "./cmd/goldilocks")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=go1.25.6", "GOOS="+t.OS, "GOARCH="+t.Arch)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", t.Dir, err)
		}
	}
	var sums []string
	for _, t := range targets {
		data, err := os.ReadFile(filepath.Join(stage, t.Dir, t.Name))
		if err != nil {
			return err
		}
		path := filepath.Join("bin", t.Dir, t.Name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0755); err != nil {
			return err
		}
		if err := os.Chmod(path, 0755); err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		sums = append(sums, fmt.Sprintf("%x  %s/%s", digest, t.Dir, t.Name))
	}
	sort.Strings(sums)
	return os.WriteFile("bin/SHA256SUMS", []byte(strings.Join(sums, "\n")+"\n"), 0644)
}

func check() error {
	var sums []string
	for _, t := range targets {
		path := filepath.Join("bin", t.Dir, t.Name)
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
		sums = append(sums, fmt.Sprintf("%x  %s/%s", digest, t.Dir, t.Name))
	}
	sort.Strings(sums)
	actual, err := os.ReadFile("bin/SHA256SUMS")
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, []byte(strings.Join(sums, "\n")+"\n")) {
		return errors.New("bin/SHA256SUMS mismatch")
	}
	fmt.Println("verified six bundled executable formats and SHA256SUMS")
	return nil
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
	binary := filepath.Join(root, "bin", t.Dir, t.Name)
	for _, path := range []string{filepath.Join("bin", t.Dir, t.Name), filepath.Join("skills", "model-routing", "SKILL.md")} {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		destination := filepath.Join(root, path)
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
		return errors.New("bundled version mismatch")
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
	for _, event := range []string{"SessionStart", "SubagentStart", "SubagentStop"} {
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
			"session_id": "00000000-0000-4000-8000-000000000001", "agent_id": "00000000-0000-4000-8000-000000000002"})
		cmd := exec.Command(launcher[0], launcher[1:]...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "PLUGIN_ROOT="+root)
		cmd.Stdin = bytes.NewReader(payload)
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("%s smoke: %w", event, err)
		}
		if event == "SubagentStop" {
			if len(bytes.TrimSpace(output)) != 0 {
				return errors.New("unregistered child was affected")
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
	if err := os.Remove(binary); err != nil {
		return err
	}
	command := config.Hooks["SessionStart"][0].Hooks[0]
	launcher := []string{"/bin/sh", "-c", command.Command}
	if runtime.GOOS == "windows" {
		launcher = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command.CommandWindows}
	}
	cmd := exec.Command(launcher[0], launcher[1:]...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PLUGIN_ROOT="+root)
	output, err = cmd.CombinedOutput()
	if err == nil || len(bytes.TrimSpace(output)) == 0 {
		return errors.New("missing bundled CLI must fail with a diagnostic")
	}
	fmt.Printf("native smoke verified %s/%s: version %s, three hook events, missing-binary diagnostic\n", t.OS, t.Arch, version)
	return nil
}
