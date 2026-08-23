package servicecontrol

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Scope string

const (
	ScopeUser   Scope = "user"
	ScopeSystem Scope = "system"
)

type InstallOptions struct {
	Name       string
	Scope      Scope
	Addr       string
	BinaryPath string
	WorkingDir string
	HomeDir    string
	ProjectDir string
	LogPath    string

	GOMEMLIMIT  string
	GOGC        string
	GOMAXPROCS  string
	GODEBUG     string
	WatchdogSec int
	MemoryHigh  string
	MemoryMax   string
}

type Status struct {
	Name        string `json:"name"`
	Scope       Scope  `json:"scope"`
	OS          string `json:"os"`
	Managed     bool   `json:"managed"`
	Installed   bool   `json:"installed"`
	Running     bool   `json:"running"`
	ServiceFile string `json:"service_file,omitempty"`
	LogFile     string `json:"log_file,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type Controller struct {
	runner commandRunner
}

type commandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
	Start(context.Context, string, ...string) error
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (osCommandRunner) Start(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Start()
}

func NewController() *Controller {
	return &Controller{runner: osCommandRunner{}}
}

func CurrentOptions() InstallOptions {
	return InstallOptions{
		Name:  strings.TrimSpace(os.Getenv("GODEX_SERVICE_NAME")),
		Scope: Scope(strings.TrimSpace(os.Getenv("GODEX_SERVICE_SCOPE"))),
	}
}

const shellEnvironmentMarker = "__GODEX_SHELL_ENVIRONMENT_V1__\x00"

// ImportUserShellEnvironment refreshes a user-scoped service process from the
// user's login shell. Service managers such as launchd and systemd commonly
// start with a minimal PATH, so merely inheriting the GoDex process environment
// is insufficient for tools installed by Homebrew, asdf, mise, nvm, Go, etc.
//
// The shell is both login and interactive so the same profile/rc files as a
// terminal session are loaded. NUL-delimited output tolerates multiline values
// and incidental text printed by shell startup files.
func ImportUserShellEnvironment(ctx context.Context) error {
	if Scope(strings.TrimSpace(os.Getenv("GODEX_SERVICE_SCOPE"))) != ScopeUser {
		return nil
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, shell, "-lic", "printf '__GODEX_SHELL_ENVIRONMENT_V1__\\0'; env -0")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("load user shell environment via %s: %w", shell, err)
	}
	for key, value := range parseNullEnvironment(output) {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set shell environment %s: %w", key, err)
		}
	}
	return nil
}

func parseNullEnvironment(output []byte) map[string]string {
	env := make(map[string]string)
	marker := []byte(shellEnvironmentMarker)
	if markerAt := bytes.Index(output, marker); markerAt >= 0 {
		output = output[markerAt+len(marker):]
	}
	for _, item := range bytes.Split(output, []byte{0}) {
		key, value, ok := strings.Cut(string(item), "=")
		if !ok || key == "" || strings.ContainsAny(key, "\r\n") {
			continue
		}
		env[key] = value
	}
	return env
}

func (c *Controller) Install(ctx context.Context, opts InstallOptions) (Status, error) {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(opts.LogPath), 0755); err != nil {
		return Status{}, err
	}
	switch runtime.GOOS {
	case "darwin":
		path, err := launchdPlistPath(opts)
		if err != nil {
			return Status{}, err
		}
		rendered, err := RenderLaunchdPlist(opts)
		if err != nil {
			return Status{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return Status{}, err
		}
		if err := os.WriteFile(path, rendered, 0644); err != nil {
			return Status{}, err
		}
	case "linux":
		path, err := systemdUnitPath(opts)
		if err != nil {
			return Status{}, err
		}
		rendered, err := RenderSystemdUnit(opts)
		if err != nil {
			return Status{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return Status{}, err
		}
		if err := os.WriteFile(path, rendered, 0644); err != nil {
			return Status{}, err
		}
		if err := c.systemctl(ctx, opts, "daemon-reload"); err != nil {
			return Status{}, err
		}
		if err := c.systemctl(ctx, opts, "enable", systemdUnitName(opts)); err != nil {
			return Status{}, err
		}
	case "windows":
		if opts.Scope == ScopeSystem {
			if _, err := c.runner.Run(ctx, "sc.exe", "create", opts.Name, "binPath=", windowsServeCommand(opts), "start=", "auto", "DisplayName=", "GoDex "+opts.Name); err != nil {
				return Status{}, err
			}
		} else {
			scriptPath, err := writeWindowsServeScript(opts)
			if err != nil {
				return Status{}, err
			}
			// /TR has a hard 261-character limit; point it at the short launcher
			// script instead of the long inline command.
			if _, err := c.runner.Run(ctx, "schtasks", "/Create", "/F", "/SC", "ONLOGON", "/TN", windowsTaskName(opts), "/TR", windowsQuote(scriptPath)); err != nil {
				return Status{}, err
			}
		}
	default:
		return Status{}, fmt.Errorf("service management is not supported on %s", runtime.GOOS)
	}
	status, err := c.Status(ctx, opts)
	if err != nil {
		return installedStatus(opts), nil
	}
	return status, nil
}

func (c *Controller) Uninstall(ctx context.Context, opts InstallOptions) (Status, error) {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return Status{}, err
	}
	_, _ = c.Stop(ctx, opts)
	switch runtime.GOOS {
	case "darwin":
		path, err := launchdPlistPath(opts)
		if err != nil {
			return Status{}, err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return Status{}, err
		}
	case "linux":
		_ = c.systemctl(ctx, opts, "disable", systemdUnitName(opts))
		path, err := systemdUnitPath(opts)
		if err != nil {
			return Status{}, err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return Status{}, err
		}
		_ = c.systemctl(ctx, opts, "daemon-reload")
	case "windows":
		if opts.Scope == ScopeSystem {
			_, err = c.runner.Run(ctx, "sc.exe", "delete", opts.Name)
		} else {
			_, err = c.runner.Run(ctx, "schtasks", "/Delete", "/F", "/TN", windowsTaskName(opts))
			if scriptErr := os.Remove(windowsServeScriptPath(opts)); scriptErr != nil && !os.IsNotExist(scriptErr) {
				if err == nil {
					err = scriptErr
				}
			}
		}
		if err != nil {
			return Status{}, err
		}
	default:
		return Status{}, fmt.Errorf("service management is not supported on %s", runtime.GOOS)
	}
	return Status{Name: opts.Name, Scope: opts.Scope, OS: runtime.GOOS, Managed: false, Installed: false, ServiceFile: serviceFileForStatus(opts), LogFile: opts.LogPath}, nil
}

func (c *Controller) Start(ctx context.Context, opts InstallOptions) (Status, error) {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return Status{}, err
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := c.runner.Run(ctx, "launchctl", "print", launchdServiceTarget(opts)); err != nil {
			if _, err := c.runner.Run(ctx, "launchctl", "bootstrap", launchdDomain(opts), mustServiceFile(opts)); err != nil {
				return Status{}, err
			}
		}
		if _, err := c.runner.Run(ctx, "launchctl", "kickstart", "-k", launchdServiceTarget(opts)); err != nil {
			return Status{}, err
		}
	case "linux":
		if err := c.systemctl(ctx, opts, "start", systemdUnitName(opts)); err != nil {
			return Status{}, err
		}
	case "windows":
		if opts.Scope == ScopeSystem {
			_, err = c.runner.Run(ctx, "sc.exe", "start", opts.Name)
		} else {
			_, err = c.runner.Run(ctx, "schtasks", "/Run", "/TN", windowsTaskName(opts))
		}
		if err != nil {
			return Status{}, err
		}
	default:
		return Status{}, fmt.Errorf("service management is not supported on %s", runtime.GOOS)
	}
	return c.Status(ctx, opts)
}

func (c *Controller) Stop(ctx context.Context, opts InstallOptions) (Status, error) {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return Status{}, err
	}
	switch runtime.GOOS {
	case "darwin":
		_, err = c.runner.Run(ctx, "launchctl", "bootout", launchdServiceTarget(opts))
	case "linux":
		err = c.systemctl(ctx, opts, "stop", systemdUnitName(opts))
	case "windows":
		if opts.Scope == ScopeSystem {
			_, err = c.runner.Run(ctx, "sc.exe", "stop", opts.Name)
		} else {
			_, err = c.runner.Run(ctx, "schtasks", "/End", "/TN", windowsTaskName(opts))
		}
	default:
		return Status{}, fmt.Errorf("service management is not supported on %s", runtime.GOOS)
	}
	if err != nil {
		return Status{}, err
	}
	return c.Status(ctx, opts)
}

func (c *Controller) Restart(ctx context.Context, opts InstallOptions) (Status, error) {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return Status{}, err
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := c.runner.Run(ctx, "launchctl", "kickstart", "-k", launchdServiceTarget(opts)); err != nil {
			return Status{}, err
		}
	case "linux":
		if err := c.systemctl(ctx, opts, "restart", systemdUnitName(opts)); err != nil {
			return Status{}, err
		}
	case "windows":
		if opts.Scope == ScopeSystem {
			_, _ = c.runner.Run(ctx, "sc.exe", "stop", opts.Name)
			_, err = c.runner.Run(ctx, "sc.exe", "start", opts.Name)
		} else {
			_, _ = c.runner.Run(ctx, "schtasks", "/End", "/TN", windowsTaskName(opts))
			_, err = c.runner.Run(ctx, "schtasks", "/Run", "/TN", windowsTaskName(opts))
		}
		if err != nil {
			return Status{}, err
		}
	default:
		return Status{}, fmt.Errorf("service management is not supported on %s", runtime.GOOS)
	}
	return c.Status(ctx, opts)
}

func (c *Controller) Status(ctx context.Context, opts InstallOptions) (Status, error) {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return Status{}, err
	}
	status := installedStatus(opts)
	switch runtime.GOOS {
	case "darwin":
		_, err = c.runner.Run(ctx, "launchctl", "print", launchdServiceTarget(opts))
		status.Running = err == nil
		status.Detail = detailFromError(err)
		if _, statErr := os.Stat(status.ServiceFile); statErr != nil {
			status.Installed = false
			if os.IsNotExist(statErr) && status.Detail == "" {
				status.Detail = "service file not found"
			}
		}
	case "linux":
		out, err := c.systemctlOutput(ctx, opts, "is-active", systemdUnitName(opts))
		status.Running = err == nil && strings.TrimSpace(string(out)) == "active"
		status.Detail = strings.TrimSpace(string(out))
		if err != nil && status.Detail == "" {
			status.Detail = err.Error()
		}
		if _, statErr := os.Stat(status.ServiceFile); statErr != nil {
			status.Installed = false
			if os.IsNotExist(statErr) && status.Detail == "" {
				status.Detail = "service file not found"
			}
		}
	case "windows":
		if opts.Scope == ScopeSystem {
			out, err := c.runner.Run(ctx, "sc.exe", "query", opts.Name)
			status.Installed = err == nil
			status.Running = err == nil && strings.Contains(strings.ToUpper(string(out)), "RUNNING")
			status.Detail = strings.TrimSpace(string(out))
		} else {
			out, err := c.runner.Run(ctx, "schtasks", "/Query", "/TN", windowsTaskName(opts))
			status.Installed = err == nil
			status.Running = err == nil && strings.Contains(strings.ToLower(string(out)), "running")
			status.Detail = strings.TrimSpace(string(out))
		}
	default:
		return Status{}, fmt.Errorf("service management is not supported on %s", runtime.GOOS)
	}
	return status, nil
}

func (c *Controller) Logs(ctx context.Context, opts InstallOptions, follow bool, stdout, stderr io.Writer) error {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		args := []string{}
		if opts.Scope == ScopeUser {
			args = append(args, "--user")
		}
		args = append(args, "-u", systemdUnitName(opts), "-n", "100")
		if follow {
			args = append(args, "-f")
		}
		cmd = exec.CommandContext(ctx, "journalctl", args...)
	case "darwin":
		args := []string{"-n", "100", opts.LogPath}
		if follow {
			args = append(args, "-f")
		}
		cmd = exec.CommandContext(ctx, "tail", args...)
	default:
		args := []string{"-NoProfile", "-Command", fmt.Sprintf("Get-Content -Path %s -Tail 100%s", powershellQuote(opts.LogPath), ternaryString(follow, " -Wait", ""))}
		cmd = exec.CommandContext(ctx, "powershell", args...)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func NormalizeOptions(opts InstallOptions) (InstallOptions, error) {
	opts.Name = sanitizeName(defaultString(opts.Name, "godex"))
	if opts.Scope == "" {
		opts.Scope = ScopeUser
	}
	if opts.Scope != ScopeUser && opts.Scope != ScopeSystem {
		return opts, fmt.Errorf("unsupported service scope %q", opts.Scope)
	}
	opts.Addr = defaultString(opts.Addr, "127.0.0.1:8088")
	if strings.TrimSpace(opts.BinaryPath) == "" {
		exe, err := os.Executable()
		if err != nil {
			return opts, err
		}
		opts.BinaryPath = exe
	}
	var err error
	opts.BinaryPath, err = filepath.Abs(opts.BinaryPath)
	if err != nil {
		return opts, err
	}
	if opts.WorkingDir == "" {
		opts.WorkingDir, _ = os.Getwd()
	}
	opts.WorkingDir, err = filepath.Abs(opts.WorkingDir)
	if err != nil {
		return opts, err
	}
	if opts.ProjectDir == "" {
		opts.ProjectDir = opts.WorkingDir
	}
	if opts.HomeDir == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			opts.HomeDir = filepath.Join(home, ".godex")
		}
	}
	if opts.LogPath == "" {
		opts.LogPath = filepath.Join(opts.HomeDir, "log", opts.Name+".service.log")
	}
	opts.GOMEMLIMIT = defaultString(opts.GOMEMLIMIT, "220MiB")
	opts.GOGC = defaultString(opts.GOGC, "50")
	opts.GOMAXPROCS = defaultString(opts.GOMAXPROCS, "1")
	opts.GODEBUG = defaultString(opts.GODEBUG, "madvdontneed=1")
	if opts.WatchdogSec <= 0 {
		opts.WatchdogSec = 30
	}
	return opts, nil
}

func RenderSystemdUnit(opts InstallOptions) ([]byte, error) {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=GoDex Web UI\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("StartLimitIntervalSec=0\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=notify\n")
	b.WriteString("NotifyAccess=all\n")
	b.WriteString("WorkingDirectory=" + systemdEscape(opts.WorkingDir) + "\n")
	b.WriteString("Environment=GODEX_HOME=" + systemdEscape(opts.HomeDir) + "\n")
	b.WriteString("Environment=GODEX_PROJECT_DIR=" + systemdEscape(opts.ProjectDir) + "\n")
	b.WriteString("Environment=GODEX_SERVICE_NAME=" + systemdEscape(opts.Name) + "\n")
	b.WriteString("Environment=GODEX_SERVICE_SCOPE=" + systemdEscape(string(opts.Scope)) + "\n")
	b.WriteString("Environment=GOMEMLIMIT=" + systemdEscape(opts.GOMEMLIMIT) + "\n")
	b.WriteString("Environment=GOGC=" + systemdEscape(opts.GOGC) + "\n")
	b.WriteString("Environment=GOMAXPROCS=" + systemdEscape(opts.GOMAXPROCS) + "\n")
	b.WriteString("Environment=GODEBUG=" + systemdEscape(opts.GODEBUG) + "\n")
	b.WriteString("ExecStart=" + systemdEscape(opts.BinaryPath) + " serve --addr " + systemdEscape(opts.Addr) + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=3\n")
	b.WriteString("WatchdogSec=" + strconv.Itoa(opts.WatchdogSec) + "\n")
	b.WriteString("MemoryAccounting=yes\n")
	if strings.TrimSpace(opts.MemoryHigh) != "" {
		b.WriteString("MemoryHigh=" + systemdEscape(opts.MemoryHigh) + "\n")
	}
	if strings.TrimSpace(opts.MemoryMax) != "" {
		b.WriteString("MemoryMax=" + systemdEscape(opts.MemoryMax) + "\n")
	}
	b.WriteString("\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return []byte(b.String()), nil
}

func RenderLaunchdPlist(opts InstallOptions) ([]byte, error) {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	label := launchdLabel(opts)
	env := map[string]string{
		"GODEX_HOME":          opts.HomeDir,
		"GODEX_PROJECT_DIR":   opts.ProjectDir,
		"GODEX_SERVICE_NAME":  opts.Name,
		"GODEX_SERVICE_SCOPE": string(opts.Scope),
		"GOMEMLIMIT":          opts.GOMEMLIMIT,
		"GOGC":                opts.GOGC,
		"GOMAXPROCS":          opts.GOMAXPROCS,
		"GODEBUG":             opts.GODEBUG,
	}
	plist := launchdPlist{
		Version:              "1.0",
		Label:                label,
		ProgramArguments:     []string{opts.BinaryPath, "serve", "--addr", opts.Addr},
		WorkingDirectory:     opts.WorkingDir,
		RunAtLoad:            true,
		KeepAlive:            true,
		EnvironmentVariables: env,
		StandardOutPath:      opts.LogPath,
		StandardErrorPath:    strings.TrimSuffix(opts.LogPath, filepath.Ext(opts.LogPath)) + ".err.log",
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	buf.WriteString(`<plist version="1.0">` + "\n")
	writeLaunchdDict(&buf, plist)
	buf.WriteString("</plist>\n")
	return buf.Bytes(), nil
}

type launchdPlist struct {
	Version              string
	Label                string
	ProgramArguments     []string
	WorkingDirectory     string
	RunAtLoad            bool
	KeepAlive            bool
	EnvironmentVariables map[string]string
	StandardOutPath      string
	StandardErrorPath    string
}

func writeLaunchdDict(buf *bytes.Buffer, plist launchdPlist) {
	buf.WriteString("<dict>\n")
	writeKeyString(buf, "Label", plist.Label)
	writeKeyArray(buf, "ProgramArguments", plist.ProgramArguments)
	writeKeyString(buf, "WorkingDirectory", plist.WorkingDirectory)
	writeKeyDict(buf, "EnvironmentVariables", plist.EnvironmentVariables)
	writeKeyBool(buf, "RunAtLoad", plist.RunAtLoad)
	writeKeyBool(buf, "KeepAlive", plist.KeepAlive)
	writeKeyString(buf, "StandardOutPath", plist.StandardOutPath)
	writeKeyString(buf, "StandardErrorPath", plist.StandardErrorPath)
	buf.WriteString("</dict>\n")
}

func writeKeyString(buf *bytes.Buffer, key, value string) {
	buf.WriteString("<key>" + xmlEscape(key) + "</key>\n<string>" + xmlEscape(value) + "</string>\n")
}

func writeKeyArray(buf *bytes.Buffer, key string, values []string) {
	buf.WriteString("<key>" + xmlEscape(key) + "</key>\n<array>\n")
	for _, value := range values {
		buf.WriteString("<string>" + xmlEscape(value) + "</string>\n")
	}
	buf.WriteString("</array>\n")
}

func writeKeyDict(buf *bytes.Buffer, key string, values map[string]string) {
	buf.WriteString("<key>" + xmlEscape(key) + "</key>\n<dict>\n")
	for _, name := range []string{"GODEX_HOME", "GODEX_PROJECT_DIR", "GODEX_SERVICE_NAME", "GODEX_SERVICE_SCOPE", "GOMEMLIMIT", "GOGC", "GOMAXPROCS", "GODEBUG"} {
		writeKeyString(buf, name, values[name])
	}
	buf.WriteString("</dict>\n")
}

func writeKeyBool(buf *bytes.Buffer, key string, value bool) {
	buf.WriteString("<key>" + xmlEscape(key) + "</key>\n")
	if value {
		buf.WriteString("<true/>\n")
	} else {
		buf.WriteString("<false/>\n")
	}
}

func xmlEscape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func (c *Controller) systemctl(ctx context.Context, opts InstallOptions, args ...string) error {
	_, err := c.systemctlOutput(ctx, opts, args...)
	return err
}

func (c *Controller) systemctlOutput(ctx context.Context, opts InstallOptions, args ...string) ([]byte, error) {
	if opts.Scope == ScopeUser {
		args = append([]string{"--user"}, args...)
	}
	return c.runner.Run(ctx, "systemctl", args...)
}

func installedStatus(opts InstallOptions) Status {
	return Status{
		Name:        opts.Name,
		Scope:       opts.Scope,
		OS:          runtime.GOOS,
		Managed:     true,
		Installed:   true,
		ServiceFile: serviceFileForStatus(opts),
		LogFile:     opts.LogPath,
	}
}

func serviceFileForStatus(opts InstallOptions) string {
	switch runtime.GOOS {
	case "darwin":
		path, _ := launchdPlistPath(opts)
		return path
	case "linux":
		path, _ := systemdUnitPath(opts)
		return path
	case "windows":
		if opts.Scope == ScopeSystem {
			return "Windows Service: " + opts.Name
		}
		return "Scheduled Task: " + windowsTaskName(opts)
	default:
		return ""
	}
}

func launchdLabel(opts InstallOptions) string {
	return "com.godex." + opts.Name
}

func launchdPlistPath(opts InstallOptions) (string, error) {
	if opts.Scope == ScopeSystem {
		return filepath.Join("/Library/LaunchDaemons", launchdLabel(opts)+".plist"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel(opts)+".plist"), nil
}

func launchdDomain(opts InstallOptions) string {
	if opts.Scope == ScopeSystem {
		return "system"
	}
	return "gui/" + strconv.Itoa(os.Getuid())
}

func launchdServiceTarget(opts InstallOptions) string {
	return launchdDomain(opts) + "/" + launchdLabel(opts)
}

func systemdUnitName(opts InstallOptions) string {
	return opts.Name + ".service"
}

func systemdUnitPath(opts InstallOptions) (string, error) {
	if opts.Scope == ScopeSystem {
		return filepath.Join("/etc/systemd/system", systemdUnitName(opts)), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName(opts)), nil
}

func windowsTaskName(opts InstallOptions) string {
	return `GoDex\` + opts.Name
}

func windowsServeCommandParts(opts InstallOptions) []string {
	return []string{
		"cd /d " + windowsQuote(opts.WorkingDir),
		`set "GODEX_HOME=` + opts.HomeDir + `"`,
		`set "GODEX_PROJECT_DIR=` + opts.ProjectDir + `"`,
		`set "GODEX_SERVICE_NAME=` + opts.Name + `"`,
		`set "GODEX_SERVICE_SCOPE=` + string(opts.Scope) + `"`,
		`set "GOMEMLIMIT=` + opts.GOMEMLIMIT + `"`,
		`set "GOGC=` + opts.GOGC + `"`,
		`set "GOMAXPROCS=` + opts.GOMAXPROCS + `"`,
		`set "GODEBUG=` + opts.GODEBUG + `"`,
		windowsQuote(opts.BinaryPath) + " serve --addr " + opts.Addr,
	}
}

func windowsServeCommand(opts InstallOptions) string {
	return `cmd /c "` + strings.Join(windowsServeCommandParts(opts), " && ") + `"`
}

// windowsServeScriptPath returns where the user-scope launcher .cmd lives.
func windowsServeScriptPath(opts InstallOptions) string {
	return filepath.Join(opts.HomeDir, opts.Name+".cmd")
}

// writeWindowsServeScript writes the launcher script that user-scope Windows
// tasks point at via schtasks /TR. The /TR value is limited to 261 characters,
// which the full inline serve command easily exceeds, so the long command body
// lives in this file and /TR only carries the short script path.
func writeWindowsServeScript(opts InstallOptions) (string, error) {
	if err := os.MkdirAll(opts.HomeDir, 0755); err != nil {
		return "", err
	}
	path := windowsServeScriptPath(opts)
	if err := os.WriteFile(path, []byte(windowsServeScript(opts)), 0644); err != nil {
		return "", err
	}
	return path, nil
}

// windowsServeScript renders the batch script body used by user-scope Windows
// tasks (a .cmd run through cmd.exe when the task fires).
func windowsServeScript(opts InstallOptions) string {
	lines := append([]string{"@echo off"}, windowsServeCommandParts(opts)...)
	return strings.Join(lines, "\r\n") + "\r\n"
}

func mustServiceFile(opts InstallOptions) string {
	path := serviceFileForStatus(opts)
	if path == "" {
		return opts.Name
	}
	return path
}

func systemdEscape(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, " \t\n\"'\\") {
		return strconv.Quote(value)
	}
	return value
}

func sanitizeName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "godex"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "godex"
	}
	return b.String()
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func detailFromError(err error) string {
	if err == nil {
		return "running"
	}
	return err.Error()
}

func powershellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func windowsQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func ternaryString(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}
