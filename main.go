// godc - Dreamcast Go CLI
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
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
	"linux/amd64":  "dreamcast-toolchain-" + tcVer + "-linux-x86_64.tar.gz",
	"linux/arm64":  "dreamcast-toolchain-" + tcVer + "-linux-aarch64.tar.gz",
}

// Build-time variables (injected via -ldflags)
var (
	version = "0.2.7"
	commit  = "unknown"
	date    = "unknown"
)

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
	defer func() { _ = f.Close() }()

	if err := toml.NewEncoder(f).Encode(a.cfg); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	return nil
}

func (a *App) env() []string {
	k := a.cfg.kos()
	environSh := filepath.Join(k, "environ.sh")

	// Try to source environ.sh and capture all environment variables
	if _, err := a.fs.Stat(environSh); err == nil {
		// Source environ.sh and print all env vars
		cmd := fmt.Sprintf("source %s && env", environSh)
		out, err := a.runner.Output("bash", "-c", cmd)
		if err == nil {
			m := make(map[string]string)
			for _, line := range strings.Split(string(out), "\n") {
				if i := strings.IndexByte(line, '='); i > 0 {
					m[line[:i]] = line[i+1:]
				}
			}
			// Ensure PATH includes toolchain and build wrappers
			ccBase := filepath.Join(a.cfg.Path, "sh-elf")
			binDir := filepath.Join(ccBase, "bin")
			wrappers := filepath.Join(k, "utils", "build_wrappers")
			if path, ok := m["PATH"]; ok {
				m["PATH"] = binDir + string(os.PathListSeparator) + wrappers + string(os.PathListSeparator) + path
			} else {
				m["PATH"] = binDir + string(os.PathListSeparator) + wrappers + string(os.PathListSeparator) + os.Getenv("PATH")
			}
			r := make([]string, 0, len(m))
			for k, v := range m {
				r = append(r, k+"="+v)
			}
			return r
		}
	}

	// Fallback: manually construct environment if environ.sh doesn't exist or fails
	ccBase := filepath.Join(a.cfg.Path, "sh-elf")
	binDir := filepath.Join(ccBase, "bin")
	gccPath := filepath.Join(binDir, "sh-elf-gcc")
	m := map[string]string{
		"KOS_BASE":    k,
		"KOS_CC_BASE": ccBase,
		"KOS_PORTS":   filepath.Join(a.cfg.Path, "kos-ports"),
		"KOS_ARCH":    "dreamcast",
		"KOS_SUBARCH": "pristine",
		"CC":          gccPath,
		"KOS_CC":      gccPath,
		"KOS_AS":      filepath.Join(binDir, "sh-elf-as"),
		"KOS_LD":      filepath.Join(binDir, "sh-elf-ld"),
		"KOS_AR":      filepath.Join(binDir, "sh-elf-ar"),
		"KOS_OBJCOPY": filepath.Join(binDir, "sh-elf-objcopy"),
		"KOS_STRIP":   filepath.Join(binDir, "sh-elf-strip"),
		"KOS_AFLAGS":  "-little",
		"PATH":        binDir + string(os.PathListSeparator) + filepath.Join(k, "utils", "build_wrappers") + string(os.PathListSeparator) + os.Getenv("PATH"),
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
		{".Makefile", execTemplate(mkTmpl, map[string]string{"Name": name, "Module": name}), true}, // always overwrite to ensure compiler path is correct
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
// Run builds and runs the project
// If ip is non-empty, uses dc-tool-ip with that IP address
// If ip is empty, uses the emulator
func (a *App) Run(ip string) error {
	tmp, err := a.fs.MkdirTemp("", "godc-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() { _ = a.fs.RemoveAll(tmp) }()

	elf := filepath.Join(tmp, "game.elf")
	if err := a.Build(elf); err != nil {
		return err
	}

	if ip != "" {
		_, _ = fmt.Fprintf(a.stdout, "Uploading to %s...\n", ip)
		return a.sh("dc-tool-ip", []string{"-t", ip, "-x", elf}, "", a.env())
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
	defer func() { _ = a.fs.Remove(tmp) }()

	_, _ = fmt.Fprintln(a.stdout, "Downloading...")
	resp, err := a.http.Get(tcURL + "/" + f)
	if err != nil {
		return fmt.Errorf("failed to download toolchain: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

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
	_, _ = fmt.Fprintf(a.stdout, "\r%s\r", strings.Repeat(" ", 60))

	if closeErr != nil {
		if copyErr != nil {
			return fmt.Errorf("download failed: %v; close failed: %w", copyErr, closeErr)
		}
		return fmt.Errorf("failed to write temp file: %w", closeErr)
	}
	if copyErr != nil {
		return fmt.Errorf("failed to download: %w", copyErr)
	}
	_, _ = fmt.Fprintf(a.stdout, "Downloaded %dMB\n", n/1024/1024)

	if err := a.fs.MkdirAll(p, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Check if we need to strip the top-level directory
	stripComponents := 0
	needsStrip, err := a.archiveNeedsStrip(tmp)
	if err != nil {
		return fmt.Errorf("failed to inspect archive: %w", err)
	}
	if needsStrip {
		stripComponents = 1
	}

	if err := a.extractTarGz(tmp, p, stripComponents); err != nil {
		return fmt.Errorf("failed to extract: %w", err)
	}

	// Create libc.a symlink if missing (some toolchains only have libg.a)
	libDir := filepath.Join(p, "sh-elf", "sh-elf", "lib")
	libcPath := filepath.Join(libDir, "libc.a")
	libgPath := filepath.Join(libDir, "libg.a")
	if _, err := a.fs.Stat(libcPath); os.IsNotExist(err) {
		if _, err := a.fs.Stat(libgPath); err == nil {
			_ = a.fs.Symlink("libg.a", libcPath)
		}
	}

	// Create binutils symlinks if missing (older toolchains may not have these)
	binDir := filepath.Join(p, "sh-elf", "bin")
	toolsDir := filepath.Join(p, "sh-elf", "sh-elf", "bin")
	binutilsTools := []string{"ar", "as", "ld", "ld.bfd", "nm", "objcopy", "objdump", "ranlib", "readelf", "size", "strings", "strip"}
	for _, tool := range binutilsTools {
		symlinkPath := filepath.Join(binDir, "sh-elf-"+tool)
		toolPath := filepath.Join(toolsDir, tool)
		if _, err := a.fs.Stat(symlinkPath); os.IsNotExist(err) {
			if _, err := a.fs.Stat(toolPath); err == nil {
				// Create relative symlink: ../sh-elf/bin/tool
				_ = a.fs.Symlink(filepath.Join("..", "sh-elf", "bin", tool), symlinkPath)
			}
		}
	}

	// Create sh-elf-ld -> sh-elf-ld.bfd if ld doesn't exist but ld.bfd does
	ldPath := filepath.Join(binDir, "sh-elf-ld")
	ldBfdPath := filepath.Join(binDir, "sh-elf-ld.bfd")
	if _, err := a.fs.Stat(ldPath); os.IsNotExist(err) {
		if _, err := a.fs.Stat(ldBfdPath); err == nil {
			_ = a.fs.Symlink("sh-elf-ld.bfd", ldPath)
		}
	}

	// Create GCC symlinks if missing (sh-elf-gcc-15.1.0 -> sh-elf-gcc, etc.)
	gccPath := filepath.Join(binDir, "sh-elf-gcc")
	if _, err := a.fs.Stat(gccPath); os.IsNotExist(err) {
		// Find sh-elf-gcc-* version
		entries, _ := a.fs.ReadDir(binDir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "sh-elf-gcc-") && !strings.Contains(e.Name(), "ar") && !strings.Contains(e.Name(), "nm") && !strings.Contains(e.Name(), "ranlib") {
				_ = a.fs.Symlink(e.Name(), gccPath)
				break
			}
		}
	}
	gppPath := filepath.Join(binDir, "sh-elf-g++")
	cppPath := filepath.Join(binDir, "sh-elf-c++")
	if _, err := a.fs.Stat(gppPath); os.IsNotExist(err) {
		if _, err := a.fs.Stat(cppPath); err == nil {
			_ = a.fs.Symlink("sh-elf-c++", gppPath)
		}
	}

	a.cfg.Path = p

	lib := filepath.Join(p, "libgodc")
	if _, err := a.fs.Stat(lib); os.IsNotExist(err) {
		_, _ = fmt.Fprintln(a.stdout, "Cloning...")
		if err := a.sh("git", []string{"clone", repo, lib}, "", nil); err != nil {
			return fmt.Errorf("failed to clone libgodc: %w", err)
		}
	}

	// Check if pre-built libgodc exists (included in toolchain tarball)
	prebuiltLibgodc := filepath.Join(lib, "libgodc.a")
	kosLib := filepath.Join(p, "kos", "lib")
	_ = a.fs.MkdirAll(kosLib, 0755)

	if _, err := a.fs.Stat(prebuiltLibgodc); err == nil {
		// Pre-built libgodc exists, just create symlinks
		_, _ = fmt.Fprintln(a.stdout, "Using pre-built libgodc...")
	} else {
		// No pre-built libs, need to build from source
		e := a.env()
		ccPath := filepath.Join(p, "sh-elf", "bin", "sh-elf-gcc")
		makeArgs := []string{"CC=" + ccPath}

		_, _ = fmt.Fprintln(a.stdout, "Building libgodc...")
		if err := a.sh("make", makeArgs, lib, e); err != nil {
			return fmt.Errorf("failed to build libgodc: %w", err)
		}
		if err := a.sh("make", append(makeArgs, "install"), lib, e); err != nil {
			return fmt.Errorf("failed to install libgodc: %w", err)
		}
	}

	// Create symlinks to kos/lib
	for _, libName := range []string{"libgodc.a", "libgodcbegin.a"} {
		dst := filepath.Join(kosLib, libName)
		src := filepath.Join(lib, libName)
		if _, err := a.fs.Stat(dst); os.IsNotExist(err) {
			if _, err := a.fs.Stat(src); err == nil {
				_ = a.fs.Symlink(filepath.Join("..", "..", "libgodc", libName), dst)
			}
		}
	}

	// Symlink libkos.a
	libkosDst := filepath.Join(kosLib, "libkos.a")
	libkosSrc := filepath.Join(lib, "kos", "libkos.a")
	if _, err := a.fs.Stat(libkosDst); os.IsNotExist(err) {
		if _, err := a.fs.Stat(libkosSrc); err == nil {
			_ = a.fs.Symlink(filepath.Join("..", "..", "libgodc", "kos", "libkos.a"), libkosDst)
		}
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
			_, writeErr = fmt.Fprintf(rcFile, "\n# godc - Dreamcast Go toolchain\nset -gx PATH \"%s\" $PATH\nsource \"%s\" > /dev/null 2>&1\n",
				filepath.Join(p, "sh-elf", "bin"),
				filepath.Join(p, "kos", "environ.sh"))
		} else {
			_, writeErr = fmt.Fprintf(rcFile, "\n# godc - Dreamcast Go toolchain\nexport PATH=\"%s:$PATH\"\nsource \"%s\" > /dev/null 2>&1\n",
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

	_, _ = fmt.Fprintln(a.stdout, "✓ Done")
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

	// Pass CC as a Make variable to override any hardcoded compiler
	ccPath := filepath.Join(a.cfg.Path, "sh-elf", "bin", "sh-elf-gcc")
	ccArg := "CC=" + ccPath

	if err := a.sh("make", []string{"-C", lib, ccArg, "clean"}, "", e); err != nil {
		return fmt.Errorf("failed to clean libgodc: %w", err)
	}
	if err := a.sh("make", []string{"-C", lib, ccArg}, "", e); err != nil {
		return fmt.Errorf("failed to build libgodc: %w", err)
	}
	if err := a.sh("make", []string{"-C", lib, ccArg, "install"}, "", e); err != nil {
		return fmt.Errorf("failed to install libgodc: %w", err)
	}
	return nil
}

// Doctor checks the installation status
func (a *App) Doctor() error {
	binDir := filepath.Join(a.cfg.Path, "sh-elf", "bin")

	// Toolchain components (required for building)
	toolchainChecks := []struct {
		name string
		path string
	}{
		{"sh-elf-gcc", filepath.Join(binDir, "sh-elf-gcc")},
		{"sh-elf-gccgo", filepath.Join(binDir, "sh-elf-gccgo")},
		{"sh-elf-ld", filepath.Join(binDir, "sh-elf-ld")},
		{"sh-elf-ar", filepath.Join(binDir, "sh-elf-ar")},
	}

	// KOS components (required for Dreamcast development)
	kosChecks := []struct {
		name string
		path string
	}{
		{"kos", a.cfg.kos()},
		{"kos-cc", filepath.Join(a.cfg.kos(), "utils", "build_wrappers", "kos-cc")},
		{"libgodc", filepath.Join(a.cfg.kos(), "lib", "libgodc.a")},
	}

	// Library checks (required for linking)
	libDir := filepath.Join(a.cfg.Path, "sh-elf", "sh-elf", "lib")
	libChecks := []struct {
		name string
		path string
	}{
		{"libc", filepath.Join(libDir, "libc.a")},
		{"libm", filepath.Join(libDir, "libm.a")},
	}

	// System tools (should be in PATH)
	systemTools := []string{"make", "git"}

	var missing []string

	// Check toolchain
	_, _ = fmt.Fprintln(a.stdout, "Toolchain:")
	for _, check := range toolchainChecks {
		status := "✗"
		if _, err := a.fs.Stat(check.path); err == nil {
			status = "✓"
		} else {
			missing = append(missing, check.name)
		}
		_, _ = fmt.Fprintf(a.stdout, "  %s %-14s %s\n", status, check.name, check.path)
	}

	// Check KOS
	_, _ = fmt.Fprintln(a.stdout, "KOS:")
	for _, check := range kosChecks {
		status := "✗"
		if _, err := a.fs.Stat(check.path); err == nil {
			status = "✓"
		} else {
			missing = append(missing, check.name)
		}
		_, _ = fmt.Fprintf(a.stdout, "  %s %-14s %s\n", status, check.name, check.path)
	}

	// Check libraries
	_, _ = fmt.Fprintln(a.stdout, "Libraries:")
	for _, check := range libChecks {
		status := "✗"
		if _, err := a.fs.Stat(check.path); err == nil {
			status = "✓"
		} else {
			missing = append(missing, check.name)
		}
		_, _ = fmt.Fprintf(a.stdout, "  %s %-14s %s\n", status, check.name, check.path)
	}

	// Check system tools
	_, _ = fmt.Fprintln(a.stdout, "System tools:")
	for _, tool := range systemTools {
		status := "✗"
		path := tool
		if p, err := exec.LookPath(tool); err == nil {
			status = "✓"
			path = p
		} else {
			missing = append(missing, tool)
		}
		_, _ = fmt.Fprintf(a.stdout, "  %s %-14s %s\n", status, tool, path)
	}

	// Check emulator
	_, _ = fmt.Fprintln(a.stdout, "Emulator:")
	emuStatus := "✗"
	emuPath := a.cfg.Emu
	if filepath.IsAbs(a.cfg.Emu) {
		if _, err := a.fs.Stat(a.cfg.Emu); err == nil {
			emuStatus = "✓"
		}
	} else {
		if p, err := exec.LookPath(a.cfg.Emu); err == nil {
			emuStatus = "✓"
			emuPath = p
		}
	}
	if emuStatus == "✗" {
		missing = append(missing, "emulator")
	}
	_, _ = fmt.Fprintf(a.stdout, "  %s %-14s %s\n", emuStatus, "emulator", emuPath)

	// Check environment by sourcing environ.sh
	_, _ = fmt.Fprintln(a.stdout, "Environment:")
	environSh := filepath.Join(a.cfg.kos(), "environ.sh")
	if _, err := a.fs.Stat(environSh); err == nil {
		// Source environ.sh and print all env vars
		cmd := fmt.Sprintf("source %s && env", environSh)
		out, err := a.runner.Output("bash", "-c", cmd)
		if err == nil {
			envMap := make(map[string]string)
			for _, line := range strings.Split(string(out), "\n") {
				if i := strings.IndexByte(line, '='); i > 0 {
					envMap[line[:i]] = line[i+1:]
				}
			}
			// Check key environment variables that point to executables/files
			envFileChecks := []string{"KOS_CC", "KOS_AS", "KOS_LD", "KOS_AR", "KOS_OBJCOPY", "KOS_STRIP", "KOS_GENROMFS"}
			for _, name := range envFileChecks {
				if path, ok := envMap[name]; ok && path != "" {
					status := "✗"
					if _, err := a.fs.Stat(path); err == nil {
						status = "✓"
					} else {
						missing = append(missing, name)
					}
					_, _ = fmt.Fprintf(a.stdout, "  %s %-14s %s\n", status, name, path)
				}
			}
			// Show key directory variables
			envDirChecks := []string{"KOS_BASE", "KOS_CC_BASE", "KOS_PORTS"}
			for _, name := range envDirChecks {
				if path, ok := envMap[name]; ok && path != "" {
					status := "✗"
					if _, err := a.fs.Stat(path); err == nil {
						status = "✓"
					} else {
						missing = append(missing, name)
					}
					_, _ = fmt.Fprintf(a.stdout, "  %s %-14s %s\n", status, name, path)
				}
			}
		} else {
			_, _ = fmt.Fprintf(a.stdout, "  ⚠ Could not source environ.sh: %v\n", err)
		}
	} else {
		// Fallback: check paths manually
		envBinDir := filepath.Join(a.cfg.Path, "sh-elf", "bin")
		envChecks := []struct {
			name string
			path string
		}{
			{"KOS_CC", filepath.Join(envBinDir, "sh-elf-gcc")},
			{"KOS_AS", filepath.Join(envBinDir, "sh-elf-as")},
			{"KOS_LD", filepath.Join(envBinDir, "sh-elf-ld")},
			{"KOS_AR", filepath.Join(envBinDir, "sh-elf-ar")},
			{"KOS_OBJCOPY", filepath.Join(envBinDir, "sh-elf-objcopy")},
			{"KOS_STRIP", filepath.Join(envBinDir, "sh-elf-strip")},
		}
		for _, check := range envChecks {
			status := "✗"
			if _, err := a.fs.Stat(check.path); err == nil {
				status = "✓"
			} else {
				missing = append(missing, check.name)
			}
			_, _ = fmt.Fprintf(a.stdout, "  %s %-14s %s\n", status, check.name, check.path)
		}
	}

	// Show configuration
	_, _ = fmt.Fprintln(a.stdout, "Configuration:")
	_, _ = fmt.Fprintf(a.stdout, "  Path:          %s\n", a.cfg.Path)
	_, _ = fmt.Fprintf(a.stdout, "  KOS_BASE:      %s\n", a.cfg.kos())
	_, _ = fmt.Fprintf(a.stdout, "  KOS_CC_BASE:   %s\n", filepath.Join(a.cfg.Path, "sh-elf"))

	if len(missing) > 0 {
		return fmt.Errorf("missing components: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Config interactively configures the application
func (a *App) Config() error {
	r := bufio.NewReader(a.stdin)

	read := func(prompt, defaultVal string) (string, error) {
		_, _ = fmt.Fprintf(a.stdout, "%s [%s]: ", prompt, defaultVal)
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
	_, _ = fmt.Fprintf(a.stdout, "PATH=%s\nKOS=%s\n", a.cfg.Path, a.cfg.kos())
}

// Version prints the version
func (a *App) Version() {
	_, _ = fmt.Fprintf(a.stdout, "godc %s (commit: %s, built: %s)\n", version, commit, date)
}

// Clean removes generated build files from the current directory
func (a *App) Clean() error {
	cwd, err := a.fs.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Files to remove: *.o *.elf romdisk.img .Makefile go.mod
	patterns := []string{"*.o", "*.elf"}
	specificFiles := []string{"romdisk.img", ".Makefile", "go.mod"}

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
		var ip string
		args := os.Args[2:]
		for i := 0; i < len(args); i++ {
			if args[i] == "--ip" {
				// Check if next arg is an IP address (not another flag)
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					ip = args[i+1]
				} else {
					// Use config IP if no value provided
					ip = app.cfg.IP
				}
				break
			}
		}
		err = app.Run(ip)
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
			_, _ = fmt.Fprintf(pr.writer, "\r[%s] %3d%% (%dMB/%dMB)", bar, pct, mb, totalMB)
		}
	}
	return n, err
}

// extractTarGz extracts a .tar.gz file to destDir with progress reporting
// If stripComponents > 0, it removes that many leading path components
func (a *App) extractTarGz(archivePath, destDir string, stripComponents int) error {
	// First pass: count total files for progress
	total, err := a.countTarEntries(archivePath)
	if err != nil {
		return fmt.Errorf("failed to count archive entries: %w", err)
	}

	// Second pass: extract with progress
	f, err := a.fs.Open(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var extracted int
	var lastPct int

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar entry: %w", err)
		}

		// Strip leading path components
		name := hdr.Name
		if stripComponents > 0 {
			parts := strings.Split(name, "/")
			if len(parts) <= stripComponents {
				continue // Skip entries that would be stripped entirely
			}
			name = strings.Join(parts[stripComponents:], "/")
		}

		if name == "" {
			continue
		}

		target := filepath.Join(destDir, name)

		// Security check: ensure target is within destDir
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			continue // Skip potentially malicious paths
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := a.fs.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", target, err)
			}
		case tar.TypeReg:
			// Ensure parent directory exists
			if err := a.fs.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			outFile, err := a.fs.Create(target)
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", target, err)
			}

			if _, err := io.Copy(outFile, tr); err != nil {
				_ = outFile.Close()
				return fmt.Errorf("failed to write file %s: %w", target, err)
			}
			_ = outFile.Close()

			// Set file permissions
			if err := a.fs.Chmod(target, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("failed to set permissions on %s: %w", target, err)
			}
		case tar.TypeSymlink:
			// Ensure parent directory exists
			if err := a.fs.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}
			// Remove existing symlink if present
			_ = a.fs.Remove(target)
			if err := a.fs.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("failed to create symlink %s: %w", target, err)
			}
		}

		extracted++
		if total > 0 {
			pct := extracted * 100 / total
			if pct != lastPct {
				lastPct = pct
				barWidth := 30
				filled := barWidth * pct / 100
				bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
				_, _ = fmt.Fprintf(a.stdout, "\r[%s] %3d%% (%d/%d files)", bar, pct, extracted, total)
			}
		}
	}

	// Clear progress line
	_, _ = fmt.Fprintf(a.stdout, "\r%s\r", strings.Repeat(" ", 60))
	_, _ = fmt.Fprintf(a.stdout, "Extracted %d files\n", extracted)

	return nil
}

// countTarEntries counts the number of entries in a tar.gz archive
func (a *App) countTarEntries(archivePath string) (int, error) {
	f, err := a.fs.Open(archivePath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var count int
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}

// archiveNeedsStrip checks if the archive needs --strip-components=1
// Returns true if kos/ is NOT a top-level directory (i.e., it's nested)
func (a *App) archiveNeedsStrip(archivePath string) (bool, error) {
	f, err := a.fs.Open(archivePath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return false, err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, err
		}

		// Check if kos/ is at the top level
		name := hdr.Name
		if strings.HasPrefix(name, "kos/") || name == "kos" {
			return false, nil // kos is at top level, no strip needed
		}
	}

	// kos/ not found at top level, needs strip
	return true, nil
}
