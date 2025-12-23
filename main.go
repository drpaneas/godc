// godc - Dreamcast Go CLI
package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/BurntSushi/toml"
)

//go:embed templates/Makefile.tmpl
var mkTmpl string

//go:embed templates/gitignore.tmpl
var giTmpl string

//go:embed templates/go.mod.tmpl
var modTmpl string

const tcVer = "gcc15.1.0-kos2.2.1"
const tcURL = "https://github.com/drpaneas/dreamcast-toolchain-builds/releases/download/" + tcVer
const repo = "https://github.com/drpaneas/libgodc.git"

var tcFiles = map[string]string{
	"darwin/arm64": "dreamcast-toolchain-" + tcVer + "-darwin-arm64.tar.gz",
	"darwin/amd64": "dreamcast-toolchain-" + tcVer + "-darwin-x86_64.tar.gz",
	"linux/amd64":  "dreamcast-toolchain-" + tcVer + "-linux-x86_64.tar.gz",
}

// cfg holds the application configuration
type cfg struct {
	Path string
	Emu  string
	IP   string
}

func (c *cfg) kos() string { return filepath.Join(c.Path, "kos") }

// App holds the application dependencies for testability
type App struct {
	cfg    *cfg
	fs     FileSystem
	runner CommandRunner
	http   HTTPClient
	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader
}

// NewApp creates a new App with real dependencies
func NewApp() *App {
	return &App{
		fs:     RealFS{},
		runner: RealRunner{},
		http:   RealHTTP{Timeout: 30 * time.Minute},
		stdout: os.Stdout,
		stderr: os.Stderr,
		stdin:  os.Stdin,
	}
}

func (a *App) cfgPath() (string, error) {
	h, err := a.fs.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "godc", "config.toml"), nil
}

func (a *App) load() (*cfg, error) {
	var c cfg
	cfgPath, err := a.cfgPath()
	if err != nil {
		return nil, err
	}

	data, err := a.fs.ReadFile(cfgPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
	} else {
		if _, err := toml.Decode(string(data), &c); err != nil {
			return nil, fmt.Errorf("failed to decode config: %w", err)
		}
	}

	if c.Path == "" {
		// Try KOS_BASE env first, then default to ~/dreamcast
		if kos := os.Getenv("KOS_BASE"); kos != "" {
			c.Path = filepath.Dir(kos) // /opt/toolchains/dc/kos -> /opt/toolchains/dc
		} else {
			h, err := a.fs.UserHomeDir()
			if err != nil {
				return nil, err
			}
			c.Path = filepath.Join(h, "dreamcast")
		}
	}

	// Apply defaults for missing values (handles partial config files)
	if c.IP == "" {
		c.IP = "192.168.2.203"
	}
	if c.Emu == "" {
		c.Emu = findEmulator()
	}
	return &c, nil
}

// findEmulator returns the best available Dreamcast emulator
// Priority: flycast (with macOS app bundle check) > lxdream
func findEmulator() string {
	// On macOS, check for Flycast.app first
	if runtime.GOOS == "darwin" {
		macApp := "/Applications/Flycast.app/Contents/MacOS/Flycast"
		if _, err := os.Stat(macApp); err == nil {
			return macApp
		}
	}

	// Check for flycast in PATH
	if _, err := exec.LookPath("flycast"); err == nil {
		return "flycast"
	}

	// Fallback to lxdream
	if _, err := exec.LookPath("lxdream"); err == nil {
		return "lxdream"
	}

	// Default to flycast even if not found (user can install later)
	if runtime.GOOS == "darwin" {
		return "/Applications/Flycast.app/Contents/MacOS/Flycast"
	}
	return "flycast"
}

func (a *App) save() error {
	cfgPath, err := a.cfgPath()
	if err != nil {
		return err
	}

	if err := a.fs.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	f, err := a.fs.Create(cfgPath)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer f.Close()

	if err := toml.NewEncoder(f).Encode(a.cfg); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	return nil
}

func (a *App) env() []string {
	k := a.cfg.kos()
	m := map[string]string{
		"KOS_BASE":    k,
		"KOS_CC_BASE": filepath.Join(a.cfg.Path, "sh-elf"),
		"KOS_PORTS":   filepath.Join(a.cfg.Path, "kos-ports"),
		"KOS_ARCH":    "dreamcast",
		"KOS_SUBARCH": "pristine",
		"PATH":        filepath.Join(a.cfg.Path, "sh-elf", "bin") + string(os.PathListSeparator) + filepath.Join(k, "utils", "build_wrappers") + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	for _, v := range os.Environ() {
		if i := strings.IndexByte(v, '='); i > 0 {
			if _, ok := m[v[:i]]; !ok {
				m[v[:i]] = v[i+1:]
			}
		}
	}
	r := make([]string, 0, len(m))
	for k, v := range m {
		r = append(r, k+"="+v)
	}
	return r
}

func (a *App) sh(name string, args []string, dir string, env []string) error {
	return a.runner.Run(name, args, dir, env, a.stdout, a.stderr)
}

// Init initializes a new godc project in the current directory
func (a *App) Init() error {
	cwd, err := a.fs.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}
	name := filepath.Base(cwd)

	// Determine kos replace path: prefer local, fallback to remote
	kosReplace := a.getKosReplacePath()

	templates := []struct {
		filename string
		content  string
		always   bool // always overwrite
	}{
		{".Makefile", execTemplate(mkTmpl, map[string]string{"Name": name, "Module": name}), false},
		{".gitignore", giTmpl, false},
		{"go.mod", execTemplate(modTmpl, map[string]string{"Name": name, "Module": name, "KosReplace": kosReplace}), true}, // always overwrite to ensure correct module name
	}

	for _, t := range templates {
		path := filepath.Join(cwd, t.filename)
		if t.always {
			if err := a.fs.WriteFile(path, []byte(t.content), 0644); err != nil {
				return fmt.Errorf("failed to write %s: %w", t.filename, err)
			}
		} else {
			if _, err := a.fs.Stat(path); err != nil {
				if os.IsNotExist(err) {
					if err := a.fs.WriteFile(path, []byte(t.content), 0644); err != nil {
						return fmt.Errorf("failed to write %s: %w", t.filename, err)
					}
				} else {
					return fmt.Errorf("failed to stat %s: %w", t.filename, err)
				}
			}
		}
	}

	// Run go mod tidy to resolve dependencies
	if err := a.sh("go", []string{"mod", "tidy"}, cwd, a.env()); err != nil {
		return fmt.Errorf("failed to run go mod tidy: %w", err)
	}

	return nil
}

// getKosReplacePath returns the path for the kos replace directive
// It prefers local path if it exists, otherwise returns remote path
func (a *App) getKosReplacePath() string {
	localPath := filepath.Join(a.cfg.Path, "libgodc", "kos")
	if _, err := a.fs.Stat(localPath); err == nil {
		return localPath
	}
	return "github.com/drpaneas/libgodc/kos latest"
}

// Build builds the current project
func (a *App) Build(output string) error {
	cwd, err := a.fs.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	makefile := filepath.Join(cwd, ".Makefile")
	if _, err := a.fs.Stat(makefile); err != nil {
		if os.IsNotExist(err) {
			if err := a.Init(); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("failed to stat .Makefile: %w", err)
		}
	}

	args := []string{"-f", ".Makefile"}
	if output != "" {
		args = append(args, "OUTPUT="+output)
	}
	return a.sh("make", args, "", a.env())
}

// Run builds and runs the project
func (a *App) Run(useIP bool) error {
	tmp, err := a.fs.MkdirTemp("", "godc-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer a.fs.RemoveAll(tmp)

	elf := filepath.Join(tmp, "game.elf")
	if err := a.Build(elf); err != nil {
		return err
	}

	if useIP {
		return a.sh("dc-tool-ip", []string{"-t", a.cfg.IP, "-x", elf}, "", a.env())
	}
	return a.sh(a.cfg.Emu, []string{elf}, "", a.env())
}

// Setup downloads and installs the Dreamcast toolchain
func (a *App) Setup() error {
	h, err := a.fs.UserHomeDir()
	if err != nil {
		return err
	}
	p := filepath.Join(h, "dreamcast")

	entries, err := a.fs.ReadDir(p)
	if err == nil && len(entries) > 0 {
		return fmt.Errorf("%s not empty", p)
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read %s: %w", p, err)
	}

	f := tcFiles[runtime.GOOS+"/"+runtime.GOARCH]
	if f == "" {
		return fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	tmp := filepath.Join(a.fs.TempDir(), f)
	defer a.fs.Remove(tmp)

	fmt.Fprintln(a.stdout, "Downloading...")
	resp, err := a.http.Get(tcURL + "/" + f)
	if err != nil {
		return fmt.Errorf("failed to download toolchain: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s", resp.Status)
	}

	out, err := a.fs.Create(tmp)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	// Create progress reader to show download progress
	total := resp.ContentLength
	pr := &progressReader{
		reader:  resp.Body,
		total:   total,
		writer:  a.stdout,
		started: time.Now(),
	}

	n, copyErr := io.Copy(out, pr)
	closeErr := out.Close()

	// Clear progress line and show final size
	fmt.Fprintf(a.stdout, "\r%s\r", strings.Repeat(" ", 60))

	if closeErr != nil {
		if copyErr != nil {
			return fmt.Errorf("download failed: %v; close failed: %w", copyErr, closeErr)
		}
		return fmt.Errorf("failed to write temp file: %w", closeErr)
	}
	if copyErr != nil {
		return fmt.Errorf("failed to download: %w", copyErr)
	}
	fmt.Fprintf(a.stdout, "Downloaded %dMB\n", n/1024/1024)

	fmt.Fprintln(a.stdout, "Extracting...")
	if err := a.fs.MkdirAll(p, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	args := []string{"xzf", tmp, "-C", p}
	tarOutput, err := a.runner.Output("tar", "tzf", tmp)
	if err != nil {
		return fmt.Errorf("failed to inspect archive: %w", err)
	}
	outStr := string(tarOutput)
	// Check if kos/ is a top-level directory (starts the output or starts a line)
	if !strings.HasPrefix(outStr, "kos/") && !strings.Contains(outStr, "\nkos/") {
		args = append(args, "--strip-components=1")
	}
	if err := a.sh("tar", args, "", nil); err != nil {
		return fmt.Errorf("failed to extract: %w", err)
	}

	a.cfg.Path = p

	lib := filepath.Join(p, "libgodc")
	if _, err := a.fs.Stat(lib); os.IsNotExist(err) {
		fmt.Fprintln(a.stdout, "Cloning...")
		if err := a.sh("git", []string{"clone", repo, lib}, "", nil); err != nil {
			return fmt.Errorf("failed to clone libgodc: %w", err)
		}
	}

	e := a.env()
	fmt.Fprintln(a.stdout, "Building...")
	if err := a.sh("make", nil, lib, e); err != nil {
		return fmt.Errorf("failed to build libgodc: %w", err)
	}
	if err := a.sh("make", []string{"install"}, lib, e); err != nil {
		return fmt.Errorf("failed to install libgodc: %w", err)
	}

	// Update shell rc file
	shell := os.Getenv("SHELL")
	var rc string
	switch {
	case strings.Contains(shell, "bash"):
		rc = filepath.Join(h, ".bashrc")
	case strings.Contains(shell, "fish"):
		rc = filepath.Join(h, ".config", "fish", "config.fish")
	case strings.Contains(shell, "zsh"):
		rc = filepath.Join(h, ".zshrc")
	default:
		// Default to .profile for POSIX-compatible shells (sh, ksh, dash, etc.)
		rc = filepath.Join(h, ".profile")
	}

	data, _ := a.fs.ReadFile(rc)
	if !strings.Contains(string(data), "# godc") {
		if strings.Contains(shell, "fish") {
			// Ensure fish config directory exists
			if err := a.fs.MkdirAll(filepath.Dir(rc), 0755); err != nil {
				return fmt.Errorf("failed to create fish config directory: %w", err)
			}
		}
		rcFile, err := a.fs.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("failed to open shell rc: %w", err)
		}

		var writeErr error
		if strings.Contains(shell, "fish") {
			// Fish shell uses different syntax
			_, writeErr = fmt.Fprintf(rcFile, "\n# godc - Dreamcast Go toolchain\nset -gx PATH \"%s\" $PATH\nsource \"%s\"\n",
				filepath.Join(p, "sh-elf", "bin"),
				filepath.Join(p, "kos", "environ.sh"))
		} else {
			_, writeErr = fmt.Fprintf(rcFile, "\n# godc - Dreamcast Go toolchain\nexport PATH=\"%s:$PATH\"\nsource \"%s\"\n",
				filepath.Join(p, "sh-elf", "bin"),
				filepath.Join(p, "kos", "environ.sh"))
		}

		if closeErr := rcFile.Close(); closeErr != nil {
			if writeErr != nil {
				return fmt.Errorf("failed to write to shell rc: %v; close failed: %w", writeErr, closeErr)
			}
			return fmt.Errorf("failed to close shell rc: %w", closeErr)
		}
		if writeErr != nil {
			return fmt.Errorf("failed to write to shell rc: %w", writeErr)
		}
	}

	// Save config only after all operations complete successfully
	if err := a.save(); err != nil {
		return err
	}

	fmt.Fprintln(a.stdout, "✓ Done")
	return nil
}

// Update updates libgodc to the latest version
func (a *App) Update() error {
	lib := filepath.Join(a.cfg.Path, "libgodc")
	e := a.env()

	if _, err := a.fs.Stat(lib); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to stat libgodc: %w", err)
		}
		if err := a.sh("git", []string{"clone", repo, lib}, "", nil); err != nil {
			return fmt.Errorf("failed to clone libgodc: %w", err)
		}
	} else {
		if err := a.sh("git", []string{"-C", lib, "pull"}, "", nil); err != nil {
			return fmt.Errorf("failed to pull libgodc: %w", err)
		}
	}

	if err := a.sh("make", []string{"-C", lib, "clean"}, "", e); err != nil {
		return fmt.Errorf("failed to clean libgodc: %w", err)
	}
	if err := a.sh("make", []string{"-C", lib}, "", e); err != nil {
		return fmt.Errorf("failed to build libgodc: %w", err)
	}
	if err := a.sh("make", []string{"-C", lib, "install"}, "", e); err != nil {
		return fmt.Errorf("failed to install libgodc: %w", err)
	}
	return nil
}

// Doctor checks the installation status
func (a *App) Doctor() error {
	checks := []struct {
		name string
		path string
	}{
		{"kos", a.cfg.kos()},
		{"libgodc", filepath.Join(a.cfg.kos(), "lib", "libgodc.a")},
		{"sh-elf-gccgo", filepath.Join(a.cfg.Path, "sh-elf", "bin", "sh-elf-gccgo")},
		{"kos-cc", filepath.Join(a.cfg.kos(), "utils", "build_wrappers", "kos-cc")},
		{"emulator", a.cfg.Emu},
	}

	var missing []string
	for _, check := range checks {
		status := "✗"
		found := false

		// For emulator, also check PATH if not an absolute path
		if check.name == "emulator" && !filepath.IsAbs(check.path) {
			if _, err := exec.LookPath(check.path); err == nil {
				found = true
			}
		}

		// Check if file exists at path
		if !found {
			if _, err := a.fs.Stat(check.path); err == nil {
				found = true
			}
		}

		if found {
			status = "✓"
		} else {
			missing = append(missing, check.name)
		}
		fmt.Fprintf(a.stdout, "%s %-12s %s\n", status, check.name, check.path)
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing components: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Config interactively configures the application
func (a *App) Config() error {
	r := bufio.NewReader(a.stdin)

	read := func(prompt, defaultVal string) (string, error) {
		fmt.Fprintf(a.stdout, "%s [%s]: ", prompt, defaultVal)
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("failed to read input: %w", err)
		}
		if s := strings.TrimSpace(line); s != "" {
			if s == "~" {
				h, err := a.fs.UserHomeDir()
				if err != nil {
					return "", err
				}
				return h, nil
			}
			if strings.HasPrefix(s, "~/") {
				h, err := a.fs.UserHomeDir()
				if err != nil {
					return "", err
				}
				return filepath.Join(h, s[2:]), nil
			}
			return s, nil
		}
		return defaultVal, nil
	}

	path, err := read("Path", a.cfg.Path)
	if err != nil {
		return err
	}
	a.cfg.Path = path

	emu, err := read("Emu", a.cfg.Emu)
	if err != nil {
		return err
	}
	a.cfg.Emu = emu

	ip, err := read("IP", a.cfg.IP)
	if err != nil {
		return err
	}
	a.cfg.IP = ip

	return a.save()
}

// Env prints environment information
func (a *App) Env() {
	fmt.Fprintf(a.stdout, "PATH=%s\nKOS=%s\n", a.cfg.Path, a.cfg.kos())
}

// Version prints the version
func (a *App) Version() {
	fmt.Fprintln(a.stdout, "godc 0.1.0")
}

// Clean removes generated build files from the current directory
func (a *App) Clean() error {
	cwd, err := a.fs.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Files to remove: *.o *.elf romdisk.img .Makefile
	patterns := []string{"*.o", "*.elf"}
	specificFiles := []string{"romdisk.img", ".Makefile"}

	// Remove files matching patterns
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(cwd, pattern))
		if err != nil {
			return fmt.Errorf("failed to glob %s: %w", pattern, err)
		}
		for _, match := range matches {
			if err := a.fs.Remove(match); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove %s: %w", match, err)
			}
		}
	}

	// Remove specific files
	for _, file := range specificFiles {
		path := filepath.Join(cwd, file)
		if err := a.fs.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove %s: %w", file, err)
		}
	}

	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("godc: setup|config|init|build|run|clean|doctor|env|update|version")
		return
	}

	app := NewApp()
	cfg, err := app.load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app.cfg = cfg

	switch os.Args[1] {
	case "setup":
		err = app.Setup()
	case "config":
		err = app.Config()
	case "init":
		err = app.Init()
	case "build":
		var out string
		args := os.Args[2:]
		for i := 0; i < len(args); i++ {
			if args[i] == "-o" && i+1 < len(args) {
				out = args[i+1]
				break
			}
		}
		err = app.Build(out)
	case "run":
		useIP := false
		for _, a := range os.Args[2:] {
			if a == "--ip" {
				useIP = true
			}
		}
		err = app.Run(useIP)
	case "clean":
		err = app.Clean()
	case "doctor":
		err = app.Doctor()
	case "env":
		app.Env()
	case "update":
		err = app.Update()
	case "version":
		app.Version()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// execTemplate executes a template with the given data
func execTemplate(tmpl string, data map[string]string) string {
	var b bytes.Buffer
	t := template.Must(template.New("").Parse(tmpl))
	if err := t.Execute(&b, data); err != nil {
		panic(fmt.Sprintf("template execution failed: %v", err))
	}
	return b.String()
}

// progressReader wraps an io.Reader and displays download progress
type progressReader struct {
	reader  io.Reader
	total   int64
	current int64
	writer  io.Writer
	started time.Time
	lastPct int
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.current += int64(n)

	// Calculate and display progress
	if pr.total > 0 {
		pct := int(pr.current * 100 / pr.total)
		// Only update display when percentage changes
		if pct != pr.lastPct {
			pr.lastPct = pct
			barWidth := 30
			filled := barWidth * pct / 100
			bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
			mb := pr.current / 1024 / 1024
			totalMB := pr.total / 1024 / 1024
			fmt.Fprintf(pr.writer, "\r[%s] %3d%% (%dMB/%dMB)", bar, pct, mb, totalMB)
		}
	}
	return n, err
}
