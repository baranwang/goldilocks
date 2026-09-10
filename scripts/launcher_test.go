package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The real launchers execute a native probe. Only GitHub's download is replaced;
// wrong checksums, lost arguments/stdio, and broken cache reuse all fail here.
func TestLauncher(t *testing.T) {
	root := t.TempDir()
	write := func(path string, body []byte, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"goldilocks.sh", "goldilocks.ps1"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(root, "scripts", name), body, 0755)
	}
	write(filepath.Join(root, ".codex-plugin/plugin.json"), []byte(`{"version":"0.2.0"}`), 0644)
	platform := ""
	for _, target := range targets {
		if target.OS == runtime.GOOS && target.Arch == runtime.GOARCH {
			platform = target.Dir
		}
	}
	if platform == "" {
		t.Skip("unsupported native platform")
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	asset := "goldilocks-" + platform + suffix
	source := filepath.Join(root, "probe.go")
	write(source, []byte(`package main
import("fmt";"os";"path/filepath";"strings";"io";"time")
func main(){
 if strings.HasPrefix(filepath.Base(os.Args[0]),"curl") {
  var out,url string
  for i:=1;i<len(os.Args);i++ {if os.Args[i]=="--output" {i++;out=os.Args[i]} else if strings.HasPrefix(os.Args[i],"https://") {url=os.Args[i]}}
  if !strings.HasPrefix(url,"https://github.com/baranwang/goldilocks/releases/download/v0.2.0/goldilocks-") {fmt.Fprintln(os.Stderr,"unexpected URL:",url);os.Exit(1)}
  if os.Getenv("FAIL_DOWNLOAD")=="1" {os.Exit(22)}
  log,err:=os.OpenFile(os.Getenv("DOWNLOAD_LOG"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if err!=nil{panic(err)}
  fmt.Fprintln(log,url);log.Close()
  time.Sleep(200*time.Millisecond)
  data,err:=os.ReadFile(os.Getenv("PROBE"));if err!=nil{panic(err)}
  if err=os.WriteFile(out,data,0600);err!=nil{panic(err)}
  return
 }
 for _,arg:=range os.Args[1:] {fmt.Println(arg)}
 io.Copy(os.Stdout,os.Stdin)
 fmt.Fprintln(os.Stderr,"diagnostic")
 os.Exit(7)
}
`), 0644)
	fixture := filepath.Join(root, "fixture"+suffix)
	build := exec.Command("go", "build", "-o", fixture, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v %s", err, output)
	}
	probe, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "scripts/SHA256SUMS"), []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(probe), asset)), 0644)
	fakeDir := filepath.Join(root, "tools")
	write(filepath.Join(fakeDir, "curl"+suffix), probe, 0755)
	probePath := filepath.Join(root, "probe-download")
	write(probePath, probe, 0755)
	cache := filepath.Join(root, "cache with spaces")
	downloadLog := filepath.Join(root, "downloads.log")
	hookCommand := ""
	run := func(fail bool) (int, string, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", filepath.Join(root, "scripts/goldilocks.sh"), "a b", `quote"and\slash`, "", "trailing\\")
		if runtime.GOOS == "windows" {
			script := strings.ReplaceAll(filepath.Join(root, "scripts/goldilocks.ps1"), "'", "''")
			cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "& '"+script+`' 'a b' 'quote"and\slash' '' 'trailing\'; exit $LASTEXITCODE`)
		}
		if hookCommand != "" {
			cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Restricted", "-Command", hookCommand)
		}
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "PATH="+fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"), "PLUGIN_DATA="+cache, "PLUGIN_ROOT="+root, "PROBE="+probePath, "DOWNLOAD_LOG="+downloadLog, "FAIL_DOWNLOAD=")
		if fail {
			cmd.Env = append(cmd.Env, "FAIL_DOWNLOAD=1")
		}
		cmd.Stdin = strings.NewReader("stdin 中文😀 without newline")
		var out, diag bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &diag
		err := cmd.Run()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Error(err)
				code = -1
			}
		}
		return code, out.String(), diag.String()
	}
	check := func(t *testing.T, fail bool) {
		t.Helper()
		code, out, diag := run(fail)
		if code != 7 || out != "a b\nquote\"and\\slash\n\ntrailing\\\nstdin 中文😀 without newline" || !strings.Contains(diag, "diagnostic") {
			t.Errorf("code=%d stdout=%q stderr=%q", code, out, diag)
		}
	}
	if code, out, _ := run(true); code == 0 || code == 7 || out != "" {
		t.Fatalf("failed download executed: %d %q", code, out)
	}
	write(probePath, []byte("corrupt download"), 0755)
	if code, out, _ := run(false); code == 0 || code == 7 || out != "" {
		t.Fatalf("checksum failure executed: %d %q", code, out)
	}
	write(probePath, probe, 0755)
	write(downloadLog, nil, 0644)
	t.Run("concurrent cold start", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			t.Run(fmt.Sprint(i), func(t *testing.T) { t.Parallel(); check(t, false) })
		}
	})
	requests, err := os.ReadFile(downloadLog)
	if err != nil || bytes.Count(requests, []byte("\n")) != 1 {
		t.Fatalf("concurrent launchers downloaded more than once: %q %v", requests, err)
	}
	check(t, true)
	if runtime.GOOS == "windows" {
		raw, err := os.ReadFile("../hooks/hooks.json")
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Hooks map[string][]struct {
				Hooks []struct{ CommandWindows string }
			}
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		hookCommand = config.Hooks["SessionStart"][0].Hooks[0].CommandWindows
		code, out, diag := run(true)
		if code != 7 || out != "hook\nstdin 中文😀 without newline" || !strings.Contains(diag, "diagnostic") {
			t.Fatalf("configured Windows hook: code=%d stdout=%q stderr=%q", code, out, diag)
		}
		hookCommand = ""
	}

	// A damaged cached executable must never run when its replacement is unavailable.
	binary := filepath.Join(cache, "bin", "0.2.0", platform, "goldilocks"+suffix)
	write(binary, []byte("corrupt cached binary"), 0755)
	if code, out, _ := run(true); code == 0 || code == 7 || out != "" {
		t.Fatalf("corrupt cache executed: %d %q", code, out)
	}
	check(t, false)
}
