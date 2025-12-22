package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- Mock implementations ---

// mockFS implements FileSystem for testing
type mockFS struct {
	files      map[string][]byte
	dirs       map[string][]os.DirEntry
	statErrors map[string]error
	homeDir    string
	cwd        string
	tempDir    string
	tempCount  int
}

func newMockFS() *mockFS {
	return &mockFS{
		files:      make(map[string][]byte),
		dirs:       make(map[string][]os.DirEntry),
		statErrors: make(map[string]error),
		homeDir:    "/home/testuser",
		cwd:        "/home/testuser/myproject",
		tempDir:    "/tmp",
	}
}

func (m *mockFS) Stat(name string) (os.FileInfo, error) {
	if err, ok := m.statErrors[name]; ok {
		return nil, err
	}
	if _, ok := m.files[name]; ok {
		return mockFileInfo{name: filepath.Base(name), isDir: false}, nil
	}
	if _, ok := m.dirs[name]; ok {
		return mockFileInfo{name: filepath.Base(name), isDir: true}, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockFS) ReadFile(name string) ([]byte, error) {
	if data, ok := m.files[name]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	m.files[name] = data
	return nil
}

func (m *mockFS) MkdirAll(path string, perm os.FileMode) error {
	m.dirs[path] = nil
	return nil
}

func (m *mockFS) Create(name string) (io.WriteCloser, error) {
	return &mockWriteCloser{fs: m, name: name}, nil
}

func (m *mockFS) OpenFile(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
	return &mockWriteCloser{fs: m, name: name, append: flag&os.O_APPEND != 0}, nil
}

func (m *mockFS) ReadDir(name string) ([]os.DirEntry, error) {
	if entries, ok := m.dirs[name]; ok {
		return entries, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockFS) Remove(name string) error {
	delete(m.files, name)
	return nil
}

func (m *mockFS) RemoveAll(path string) error {
	for k := range m.files {
		if strings.HasPrefix(k, path) {
			delete(m.files, k)
		}
	}
	return nil
}

func (m *mockFS) MkdirTemp(dir, pattern string) (string, error) {
	m.tempCount++
	tmpPath := filepath.Join(m.tempDir, pattern+"-"+string(rune('0'+m.tempCount)))
	m.dirs[tmpPath] = nil
	return tmpPath, nil
}

func (m *mockFS) UserHomeDir() (string, error) {
	return m.homeDir, nil
}

func (m *mockFS) Getwd() (string, error) {
	return m.cwd, nil
}

func (m *mockFS) TempDir() string {
	return m.tempDir
}

// mockFileInfo implements os.FileInfo
type mockFileInfo struct {
	name  string
	isDir bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return 0 }
func (m mockFileInfo) Mode() os.FileMode  { return 0644 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return m.isDir }
func (m mockFileInfo) Sys() any           { return nil }

// mockWriteCloser implements io.WriteCloser
type mockWriteCloser struct {
	fs     *mockFS
	name   string
	buf    bytes.Buffer
	append bool
}

func (m *mockWriteCloser) Write(p []byte) (int, error) {
	return m.buf.Write(p)
}

func (m *mockWriteCloser) Close() error {
	if m.append {
		existing := m.fs.files[m.name]
		m.fs.files[m.name] = append(existing, m.buf.Bytes()...)
	} else {
		m.fs.files[m.name] = m.buf.Bytes()
	}
	return nil
}

// mockRunner implements CommandRunner for testing
type mockRunner struct {
	calls   []mockCall
	errors  map[string]error
	outputs map[string][]byte
}

type mockCall struct {
	name string
	args []string
	dir  string
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		errors:  make(map[string]error),
		outputs: make(map[string][]byte),
	}
}

func (m *mockRunner) Run(name string, args []string, dir string, env []string, stdout, stderr io.Writer) error {
	m.calls = append(m.calls, mockCall{name: name, args: args, dir: dir})
	if err, ok := m.errors[name]; ok {
		return err
	}
	return nil
}

func (m *mockRunner) Output(name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	if output, ok := m.outputs[key]; ok {
		return output, nil
	}
	return nil, nil
}

// mockHTTP implements HTTPClient for testing
type mockHTTP struct {
	responses map[string]*http.Response
	errors    map[string]error
}

func newMockHTTP() *mockHTTP {
	return &mockHTTP{
		responses: make(map[string]*http.Response),
		errors:    make(map[string]error),
	}
}

func (m *mockHTTP) Get(url string) (*http.Response, error) {
	if err, ok := m.errors[url]; ok {
		return nil, err
	}
	if resp, ok := m.responses[url]; ok {
		return resp, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte("mock content"))),
	}, nil
}

// mockDirEntry implements os.DirEntry
type mockDirEntry struct {
	name  string
	isDir bool
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() os.FileMode          { return 0 }
func (m mockDirEntry) Info() (os.FileInfo, error) { return nil, nil }

// --- Tests for NewApp ---

func TestNewApp(t *testing.T) {
	app := NewApp()
	if app.fs == nil {
		t.Error("expected fs to be set")
	}
	if app.runner == nil {
		t.Error("expected runner to be set")
	}
	if app.http == nil {
		t.Error("expected http to be set")
	}
	if app.stdout == nil {
		t.Error("expected stdout to be set")
	}
	if app.stderr == nil {
		t.Error("expected stderr to be set")
	}
	if app.stdin == nil {
		t.Error("expected stdin to be set")
	}
}

// --- Test helpers ---

func newTestApp() (*App, *mockFS, *mockRunner, *mockHTTP, *bytes.Buffer) {
	fs := newMockFS()
	runner := newMockRunner()
	httpClient := newMockHTTP()
	stdout := &bytes.Buffer{}

	app := &App{
		cfg: &cfg{
			Path: "/home/testuser/dreamcast",
			Emu:  "flycast",
			IP:   "192.168.2.203",
		},
		fs:     fs,
		runner: runner,
		http:   httpClient,
		stdout: stdout,
		stderr: &bytes.Buffer{},
		stdin:  strings.NewReader(""),
	}
	return app, fs, runner, httpClient, stdout
}

// --- Tests for config functions ---

func TestCfgPath(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	path, err := app.cfgPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join("/home/testuser", ".config", "godc", "config.toml")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestLoadEmptyConfig(t *testing.T) {
	// Save and restore KOS_BASE
	orig := os.Getenv("KOS_BASE")
	os.Unsetenv("KOS_BASE")
	defer func() {
		if orig == "" {
			os.Unsetenv("KOS_BASE")
		} else {
			os.Setenv("KOS_BASE", orig)
		}
	}()

	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"
	app.cfg = nil

	cfg, err := app.load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedPath := filepath.Join("/home/testuser", "dreamcast")
	if cfg.Path != expectedPath {
		t.Errorf("expected Path=%s, got %s", expectedPath, cfg.Path)
	}
	if cfg.IP != "192.168.2.203" {
		t.Errorf("expected IP=192.168.2.203, got %s", cfg.IP)
	}
}

func TestLoadExistingConfig(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	configPath := filepath.Join("/home/testuser", ".config", "godc", "config.toml")
	fs.files[configPath] = []byte(`Path = "/custom/path"
Emu = "custom-emu"
IP = "10.0.0.1"
`)

	cfg, err := app.load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Path != "/custom/path" {
		t.Errorf("expected Path=/custom/path, got %s", cfg.Path)
	}
	if cfg.Emu != "custom-emu" {
		t.Errorf("expected Emu=custom-emu, got %s", cfg.Emu)
	}
	if cfg.IP != "10.0.0.1" {
		t.Errorf("expected IP=10.0.0.1, got %s", cfg.IP)
	}
}

func TestLoadWithKOSBaseEnv(t *testing.T) {
	// Save and restore env
	orig := os.Getenv("KOS_BASE")
	os.Setenv("KOS_BASE", "/opt/toolchains/dc/kos")
	defer func() {
		if orig == "" {
			os.Unsetenv("KOS_BASE")
		} else {
			os.Setenv("KOS_BASE", orig)
		}
	}()

	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	cfg, err := app.load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Path != "/opt/toolchains/dc" {
		t.Errorf("expected Path=/opt/toolchains/dc, got %s", cfg.Path)
	}
}

func TestSave(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"
	app.cfg = &cfg{
		Path: "/custom/path",
		Emu:  "my-emu",
		IP:   "1.2.3.4",
	}

	err := app.save()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	configPath := filepath.Join("/home/testuser", ".config", "godc", "config.toml")
	data, ok := fs.files[configPath]
	if !ok {
		t.Fatal("config file was not created")
	}

	content := string(data)
	if !strings.Contains(content, "Path = \"/custom/path\"") {
		t.Errorf("config should contain Path, got: %s", content)
	}
}

func TestEnv(t *testing.T) {
	app, _, _, _, _ := newTestApp()
	app.cfg = &cfg{Path: "/dc"}

	env := app.env()

	// Convert to map for easier testing
	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["KOS_BASE"] != filepath.Join("/dc", "kos") {
		t.Errorf("KOS_BASE incorrect: %s", envMap["KOS_BASE"])
	}
	if envMap["KOS_CC_BASE"] != filepath.Join("/dc", "sh-elf") {
		t.Errorf("KOS_CC_BASE incorrect: %s", envMap["KOS_CC_BASE"])
	}
	if envMap["KOS_ARCH"] != "dreamcast" {
		t.Errorf("KOS_ARCH incorrect: %s", envMap["KOS_ARCH"])
	}
}

func TestEnvPreservesExistingEnv(t *testing.T) {
	app, _, _, _, _ := newTestApp()
	app.cfg = &cfg{Path: "/dc"}

	// Set a custom env var
	os.Setenv("MY_CUSTOM_VAR", "test_value")
	defer os.Unsetenv("MY_CUSTOM_VAR")

	env := app.env()

	// Convert to map for easier testing
	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["MY_CUSTOM_VAR"] != "test_value" {
		t.Errorf("custom env var not preserved: %s", envMap["MY_CUSTOM_VAR"])
	}
}

func TestCfgKos(t *testing.T) {
	c := &cfg{Path: "/home/user/dreamcast"}
	expected := filepath.Join("/home/user/dreamcast", "kos")
	if c.kos() != expected {
		t.Errorf("expected %s, got %s", expected, c.kos())
	}
}

// --- Tests for Init ---

func TestInit(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"

	err := app.Init()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that files were created
	expectedFiles := []string{
		filepath.Join("/home/testuser/myproject", ".Makefile"),
		filepath.Join("/home/testuser/myproject", ".gitignore"),
		filepath.Join("/home/testuser/myproject", "go.mod"),
	}

	for _, f := range expectedFiles {
		if _, ok := fs.files[f]; !ok {
			t.Errorf("expected file %s to be created", f)
		}
	}

	// Check go.mod content
	gomod := string(fs.files[filepath.Join("/home/testuser/myproject", "go.mod")])
	if !strings.Contains(gomod, "module myproject") {
		t.Errorf("go.mod should contain module name, got: %s", gomod)
	}
}

func TestInitSkipsExistingFiles(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"

	// Pre-create .gitignore
	gitignorePath := filepath.Join("/home/testuser/myproject", ".gitignore")
	fs.files[gitignorePath] = []byte("custom content")

	err := app.Init()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// .gitignore should not be overwritten
	if string(fs.files[gitignorePath]) != "custom content" {
		t.Error(".gitignore should not be overwritten")
	}
}

func TestInitAlwaysOverwritesGoMod(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"

	// Pre-create go.mod
	gomodPath := filepath.Join("/home/testuser/myproject", "go.mod")
	fs.files[gomodPath] = []byte("old content")

	err := app.Init()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// go.mod should be overwritten (always regenerated to ensure correct module name)
	if string(fs.files[gomodPath]) == "old content" {
		t.Error("go.mod should be overwritten")
	}
}

// --- Tests for Build ---

func TestBuild(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"

	// .Makefile exists
	fs.files[filepath.Join("/home/testuser/myproject", ".Makefile")] = []byte("...")

	err := app.Build("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that make was called
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	if runner.calls[0].name != "make" {
		t.Errorf("expected make, got %s", runner.calls[0].name)
	}
}

func TestBuildWithOutput(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"
	fs.files[filepath.Join("/home/testuser/myproject", ".Makefile")] = []byte("...")

	err := app.Build("/tmp/out.elf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check OUTPUT arg
	found := false
	for _, arg := range runner.calls[0].args {
		if arg == "OUTPUT=/tmp/out.elf" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected OUTPUT=/tmp/out.elf in args: %v", runner.calls[0].args)
	}
}

func TestBuildCallsInitIfNoMakefile(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"

	err := app.Build("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Init should have created files
	if _, ok := fs.files[filepath.Join("/home/testuser/myproject", ".Makefile")]; !ok {
		t.Error(".Makefile should be created by init")
	}

	// go mod tidy (from Init) + make should be called
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls (go mod tidy + make), got %d", len(runner.calls))
	}
}

func TestBuildMakeError(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"
	fs.files[filepath.Join("/home/testuser/myproject", ".Makefile")] = []byte("...")
	runner.errors["make"] = errors.New("make failed")

	err := app.Build("")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "make failed") {
		t.Errorf("expected make failed error, got: %v", err)
	}
}

// --- Tests for Run ---

func TestRunWithEmulator(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"
	fs.files[filepath.Join("/home/testuser/myproject", ".Makefile")] = []byte("...")

	err := app.Run(false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should call make first, then emulator
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(runner.calls))
	}
	if runner.calls[0].name != "make" {
		t.Errorf("first call should be make, got %s", runner.calls[0].name)
	}
	if runner.calls[1].name != "flycast" {
		t.Errorf("second call should be flycast, got %s", runner.calls[1].name)
	}
}

func TestRunWithIP(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"
	fs.files[filepath.Join("/home/testuser/myproject", ".Makefile")] = []byte("...")

	err := app.Run(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should use dc-tool-ip
	if len(runner.calls) < 2 {
		t.Fatalf("expected at least 2 calls, got %d", len(runner.calls))
	}
	if runner.calls[1].name != "dc-tool-ip" {
		t.Errorf("second call should be dc-tool-ip, got %s", runner.calls[1].name)
	}

	// Check IP argument
	found := false
	for _, arg := range runner.calls[1].args {
		if arg == "192.168.2.203" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected IP in args: %v", runner.calls[1].args)
	}
}

func TestRunBuildError(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"
	fs.files[filepath.Join("/home/testuser/myproject", ".Makefile")] = []byte("...")
	runner.errors["make"] = errors.New("build failed")

	err := app.Run(false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "build failed") {
		t.Errorf("expected build failed error, got: %v", err)
	}
}

func TestRunEmulatorError(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	fs.cwd = "/home/testuser/myproject"
	fs.files[filepath.Join("/home/testuser/myproject", ".Makefile")] = []byte("...")
	runner.errors["flycast"] = errors.New("emulator failed")

	err := app.Run(false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "emulator failed") {
		t.Errorf("expected emulator failed error, got: %v", err)
	}
}

// --- Tests for Setup ---

func TestSetupFailsIfDirNotEmpty(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	dcPath := filepath.Join("/home/testuser", "dreamcast")
	fs.dirs[dcPath] = []os.DirEntry{mockDirEntry{name: "existing-file"}}

	err := app.Setup()
	if err == nil {
		t.Fatal("expected error for non-empty directory")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("expected 'not empty' error, got: %v", err)
	}
}

func TestSetupUnsupportedPlatform(t *testing.T) {
	// This test only makes sense on platforms we don't support
	// Since we're likely on a supported platform, skip the actual check
	// and just verify the error path exists
	if tcFiles[runtime.GOOS+"/"+runtime.GOARCH] != "" {
		t.Skip("running on supported platform")
	}

	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	err := app.Setup()
	if err == nil {
		t.Fatal("expected error for unsupported platform")
	}
}

func TestSetupHTTPError(t *testing.T) {
	app, fs, _, httpClient, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	// Set up HTTP to return an error
	for url := range httpClient.responses {
		delete(httpClient.responses, url)
	}
	httpClient.errors[tcURL+"/"+tcFiles[runtime.GOOS+"/"+runtime.GOARCH]] = errors.New("network error")

	err := app.Setup()
	if err == nil {
		t.Fatal("expected error for HTTP failure")
	}
	if !strings.Contains(err.Error(), "network error") {
		t.Errorf("expected network error, got: %v", err)
	}
}

func TestSetupHTTP404(t *testing.T) {
	app, fs, _, httpClient, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	url := tcURL + "/" + tcFiles[runtime.GOOS+"/"+runtime.GOARCH]
	httpClient.responses[url] = &http.Response{
		StatusCode: http.StatusNotFound,
		Status:     "404 Not Found",
		Body:       io.NopCloser(bytes.NewReader([]byte(""))),
	}

	err := app.Setup()
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error, got: %v", err)
	}
}

func TestSetupSuccess(t *testing.T) {
	app, fs, runner, httpClient, stdout := newTestApp()
	fs.homeDir = "/home/testuser"

	// Mock tar output to include kos/ to avoid --strip-components
	runner.outputs["tar tzf "+filepath.Join(fs.TempDir(), tcFiles[runtime.GOOS+"/"+runtime.GOARCH])] = []byte("stuff\nkos/\nmore")

	// HTTP response with some content
	url := tcURL + "/" + tcFiles[runtime.GOOS+"/"+runtime.GOARCH]
	httpClient.responses[url] = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(make([]byte, 1024*1024))), // 1MB
	}

	err := app.Setup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check output messages
	output := stdout.String()
	if !strings.Contains(output, "Downloading") {
		t.Errorf("expected 'Downloading' in output: %s", output)
	}
	if !strings.Contains(output, "Extracting") {
		t.Errorf("expected 'Extracting' in output: %s", output)
	}
	if !strings.Contains(output, "✓ Done") {
		t.Errorf("expected '✓ Done' in output: %s", output)
	}

	// Check that commands were called
	foundTar := false
	foundGitClone := false
	foundMake := false
	for _, call := range runner.calls {
		if call.name == "tar" {
			foundTar = true
		}
		if call.name == "git" && len(call.args) > 0 && call.args[0] == "clone" {
			foundGitClone = true
		}
		if call.name == "make" {
			foundMake = true
		}
	}
	if !foundTar {
		t.Error("expected tar to be called")
	}
	if !foundGitClone {
		t.Error("expected git clone to be called")
	}
	if !foundMake {
		t.Error("expected make to be called")
	}
}

func TestSetupWithStripComponents(t *testing.T) {
	app, fs, runner, httpClient, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	// Mock tar output WITHOUT kos/ to trigger --strip-components
	runner.outputs["tar tzf "+filepath.Join(fs.TempDir(), tcFiles[runtime.GOOS+"/"+runtime.GOARCH])] = []byte("dreamcast/stuff\ndreamcast/more")

	url := tcURL + "/" + tcFiles[runtime.GOOS+"/"+runtime.GOARCH]
	httpClient.responses[url] = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte("content"))),
	}

	err := app.Setup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that tar was called with --strip-components
	for _, call := range runner.calls {
		if call.name == "tar" {
			found := false
			for _, arg := range call.args {
				if arg == "--strip-components=1" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected --strip-components=1 in tar args: %v", call.args)
			}
			break
		}
	}
}

func TestSetupWithNestedKosNeedsStrip(t *testing.T) {
	// Regression test: when kos/ appears nested (e.g., dc/kos/), we still need --strip-components
	app, fs, runner, httpClient, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	// Mock tar output with NESTED kos/ (not top-level) - should still trigger --strip-components
	runner.outputs["tar tzf "+filepath.Join(fs.TempDir(), tcFiles[runtime.GOOS+"/"+runtime.GOARCH])] = []byte("dc/\ndc/kos/\ndc/kos/utils/\ndc/sh-elf/")

	url := tcURL + "/" + tcFiles[runtime.GOOS+"/"+runtime.GOARCH]
	httpClient.responses[url] = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte("content"))),
	}

	err := app.Setup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that tar was called with --strip-components (nested kos/ requires stripping)
	for _, call := range runner.calls {
		if call.name == "tar" {
			found := false
			for _, arg := range call.args {
				if arg == "--strip-components=1" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected --strip-components=1 for nested kos/, got args: %v", call.args)
			}
			break
		}
	}
}

// --- Tests for Update ---

func TestUpdateClonesIfNotExists(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	app.cfg.Path = "/dc"

	// libgodc doesn't exist
	fs.statErrors[filepath.Join("/dc", "libgodc")] = os.ErrNotExist

	err := app.Update()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// First call should be git clone
	if len(runner.calls) < 1 {
		t.Fatal("expected at least 1 call")
	}
	if runner.calls[0].name != "git" {
		t.Errorf("expected git, got %s", runner.calls[0].name)
	}
	if len(runner.calls[0].args) < 1 || runner.calls[0].args[0] != "clone" {
		t.Errorf("expected clone, got %v", runner.calls[0].args)
	}
}

func TestUpdatePullsIfExists(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	app.cfg.Path = "/dc"

	// libgodc exists
	fs.dirs[filepath.Join("/dc", "libgodc")] = nil

	err := app.Update()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// First call should be git pull
	if len(runner.calls) < 1 {
		t.Fatal("expected at least 1 call")
	}
	if runner.calls[0].name != "git" {
		t.Errorf("expected git, got %s", runner.calls[0].name)
	}
	foundPull := false
	for _, arg := range runner.calls[0].args {
		if arg == "pull" {
			foundPull = true
		}
	}
	if !foundPull {
		t.Errorf("expected pull in args, got %v", runner.calls[0].args)
	}
}

func TestUpdateBuildsLibgodc(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	app.cfg.Path = "/dc"
	fs.dirs[filepath.Join("/dc", "libgodc")] = nil

	err := app.Update()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: git pull, make clean, make, make install
	if len(runner.calls) < 4 {
		t.Fatalf("expected 4 calls, got %d", len(runner.calls))
	}

	// Verify make calls
	makeCount := 0
	for _, call := range runner.calls {
		if call.name == "make" {
			makeCount++
		}
	}
	if makeCount != 3 {
		t.Errorf("expected 3 make calls, got %d", makeCount)
	}
}

func TestUpdateGitCloneError(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	app.cfg.Path = "/dc"
	fs.statErrors[filepath.Join("/dc", "libgodc")] = os.ErrNotExist
	runner.errors["git"] = errors.New("git failed")

	err := app.Update()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "clone") {
		t.Errorf("expected clone error, got: %v", err)
	}
}

func TestUpdateGitPullError(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	app.cfg.Path = "/dc"
	fs.dirs[filepath.Join("/dc", "libgodc")] = nil
	runner.errors["git"] = errors.New("pull failed")

	err := app.Update()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "pull") {
		t.Errorf("expected pull error, got: %v", err)
	}
}

func TestUpdateMakeCleanError(t *testing.T) {
	app, fs, runner, _, _ := newTestApp()
	app.cfg.Path = "/dc"
	fs.dirs[filepath.Join("/dc", "libgodc")] = nil
	runner.errors["make"] = errors.New("make failed")

	err := app.Update()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "clean") {
		t.Errorf("expected clean error, got: %v", err)
	}
}

// --- Tests for Doctor ---

func TestDoctor(t *testing.T) {
	app, fs, _, _, stdout := newTestApp()
	app.cfg.Path = "/dc"
	app.cfg.Emu = "flycast"

	// Set up all required paths to exist for a successful doctor check
	kosPath := filepath.Join("/dc", "kos")
	fs.dirs[kosPath] = nil
	fs.files[filepath.Join(kosPath, "lib", "libgodc.a")] = []byte{}
	fs.files[filepath.Join("/dc", "sh-elf", "bin", "sh-elf-gccgo")] = []byte{}
	fs.files[filepath.Join(kosPath, "utils", "build_wrappers", "kos-cc")] = []byte{}
	fs.files["flycast"] = []byte{} // emulator exists

	err := app.Doctor()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "✓ kos") {
		t.Errorf("expected ✓ kos in output: %s", output)
	}
}

func TestDoctorShowsMissing(t *testing.T) {
	app, _, _, _, stdout := newTestApp()
	app.cfg.Path = "/dc"
	app.cfg.Emu = "nonexistent"

	err := app.Doctor()
	if err == nil {
		t.Fatal("expected error for missing components")
	}

	output := stdout.String()
	if !strings.Contains(output, "✗") {
		t.Errorf("expected ✗ for missing items in output: %s", output)
	}

	// Error should list missing components
	if !strings.Contains(err.Error(), "missing components") {
		t.Errorf("expected 'missing components' in error, got: %v", err)
	}
}

// --- Tests for Config ---

func TestConfig(t *testing.T) {
	app, fs, _, _, stdout := newTestApp()
	fs.homeDir = "/home/testuser"
	app.stdin = strings.NewReader("/new/path\nnew-emu\n10.0.0.1\n")

	err := app.Config()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if app.cfg.Path != "/new/path" {
		t.Errorf("expected Path=/new/path, got %s", app.cfg.Path)
	}
	if app.cfg.Emu != "new-emu" {
		t.Errorf("expected Emu=new-emu, got %s", app.cfg.Emu)
	}
	if app.cfg.IP != "10.0.0.1" {
		t.Errorf("expected IP=10.0.0.1, got %s", app.cfg.IP)
	}

	// Check prompts were shown
	if !strings.Contains(stdout.String(), "Path") {
		t.Error("expected Path prompt in output")
	}
}

func TestConfigWithTildeOnly(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"
	app.stdin = strings.NewReader("~\n\n\n")

	err := app.Config()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if app.cfg.Path != "/home/testuser" {
		t.Errorf("expected Path=/home/testuser, got %s", app.cfg.Path)
	}
}

func TestConfigWithTildeExpansion(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"
	app.stdin = strings.NewReader("~/dreamcast\n\n\n")

	err := app.Config()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join("/home/testuser", "dreamcast")
	if app.cfg.Path != expected {
		t.Errorf("expected Path=%s, got %s", expected, app.cfg.Path)
	}
}

func TestConfigUsesDefaults(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"
	app.cfg.Path = "/original"
	app.cfg.Emu = "original-emu"
	app.cfg.IP = "1.1.1.1"
	app.stdin = strings.NewReader("\n\n\n") // Empty inputs

	err := app.Config()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if app.cfg.Path != "/original" {
		t.Errorf("expected Path=/original, got %s", app.cfg.Path)
	}
}

// --- Tests for Env ---

func TestEnvOutput(t *testing.T) {
	app, _, _, _, stdout := newTestApp()
	app.cfg.Path = "/dc"

	app.Env()

	output := stdout.String()
	if !strings.Contains(output, "PATH=/dc") {
		t.Errorf("expected PATH=/dc in output: %s", output)
	}
	if !strings.Contains(output, "KOS=") {
		t.Errorf("expected KOS= in output: %s", output)
	}
}

// --- Tests for Version ---

func TestVersion(t *testing.T) {
	app, _, _, _, stdout := newTestApp()

	app.Version()

	if !strings.Contains(stdout.String(), "godc 0.1.0") {
		t.Errorf("expected version in output: %s", stdout.String())
	}
}

// --- Tests for execTemplate ---

func TestExecTemplate(t *testing.T) {
	tmpl := "module {{.Module}}\nname={{.Name}}"
	result := execTemplate(tmpl, map[string]string{"Name": "myapp", "Module": "myapp"})

	if !strings.Contains(result, "module myapp") {
		t.Errorf("expected module myapp, got: %s", result)
	}
	if !strings.Contains(result, "name=myapp") {
		t.Errorf("expected name=myapp, got: %s", result)
	}
}

// --- Additional error path tests ---

func TestCfgPathError(t *testing.T) {
	app, _, _, _, _ := newTestApp()
	// Create a mock FS that returns an error for UserHomeDir
	errFS := &errorFS{err: errors.New("home dir error")}
	app.fs = errFS

	_, err := app.cfgPath()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadHomeDirError(t *testing.T) {
	app, _, _, _, _ := newTestApp()
	// Need to test when ReadFile succeeds but returns empty config
	// and UserHomeDir fails
	errFS := &errorFS{err: errors.New("home dir error")}
	app.fs = errFS

	_, err := app.load()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSaveCreateError(t *testing.T) {
	app, _, _, _, _ := newTestApp()
	app.cfg = &cfg{Path: "/test"}

	errFS := &errorFSCreate{mockFS: newMockFS()}
	errFS.homeDir = "/home/testuser"
	app.fs = errFS

	err := app.save()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "create config") {
		t.Errorf("expected create config error, got: %v", err)
	}
}

func TestInitWriteError(t *testing.T) {
	app, _, _, _, _ := newTestApp()

	errFS := &errorFSWrite{mockFS: newMockFS()}
	errFS.cwd = "/home/testuser/myproject"
	app.fs = errFS

	err := app.Init()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to write") {
		t.Errorf("expected write error, got: %v", err)
	}
}

func TestBuildGetwdError(t *testing.T) {
	app, _, _, _, _ := newTestApp()

	errFS := &errorFS{err: errors.New("getwd error")}
	app.fs = errFS

	err := app.Build("")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "working directory") {
		t.Errorf("expected working directory error, got: %v", err)
	}
}

func TestRunMkdirTempError(t *testing.T) {
	app, _, _, _, _ := newTestApp()

	errFS := &errorFSMkdirTemp{mockFS: newMockFS()}
	errFS.cwd = "/home/testuser/myproject"
	errFS.files[filepath.Join("/home/testuser/myproject", ".Makefile")] = []byte("...")
	app.fs = errFS

	err := app.Run(false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "temp directory") {
		t.Errorf("expected temp directory error, got: %v", err)
	}
}

// Error-returning mock filesystems

type errorFS struct {
	err error
}

func (e *errorFS) Stat(name string) (os.FileInfo, error)                      { return nil, e.err }
func (e *errorFS) ReadFile(name string) ([]byte, error)                       { return nil, e.err }
func (e *errorFS) WriteFile(name string, data []byte, perm os.FileMode) error { return e.err }
func (e *errorFS) MkdirAll(path string, perm os.FileMode) error               { return e.err }
func (e *errorFS) Create(name string) (io.WriteCloser, error)                 { return nil, e.err }
func (e *errorFS) OpenFile(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
	return nil, e.err
}
func (e *errorFS) ReadDir(name string) ([]os.DirEntry, error)    { return nil, e.err }
func (e *errorFS) Remove(name string) error                      { return e.err }
func (e *errorFS) RemoveAll(path string) error                   { return e.err }
func (e *errorFS) MkdirTemp(dir, pattern string) (string, error) { return "", e.err }
func (e *errorFS) TempDir() string                               { return "/tmp" }
func (e *errorFS) UserHomeDir() (string, error)                  { return "", e.err }
func (e *errorFS) Getwd() (string, error)                        { return "", e.err }

type errorFSCreate struct {
	*mockFS
}

func (e *errorFSCreate) Create(name string) (io.WriteCloser, error) {
	return nil, errors.New("create failed")
}

type errorFSWrite struct {
	*mockFS
}

func (e *errorFSWrite) WriteFile(name string, data []byte, perm os.FileMode) error {
	return errors.New("write failed")
}

type errorFSMkdirTemp struct {
	*mockFS
}

func (e *errorFSMkdirTemp) MkdirTemp(dir, pattern string) (string, error) {
	return "", errors.New("mkdirtemp failed")
}

type errorFSMkdirAll struct {
	*mockFS
}

func (e *errorFSMkdirAll) MkdirAll(path string, perm os.FileMode) error {
	return errors.New("mkdir failed")
}

// --- More error path tests ---

func TestSaveMkdirError(t *testing.T) {
	app, _, _, _, _ := newTestApp()
	app.cfg = &cfg{Path: "/test"}

	errFS := &errorFSMkdirAll{mockFS: newMockFS()}
	errFS.homeDir = "/home/testuser"
	app.fs = errFS

	err := app.save()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "config directory") {
		t.Errorf("expected config directory error, got: %v", err)
	}
}

func TestLoadDecodeError(t *testing.T) {
	app, fs, _, _, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	// Write invalid TOML
	configPath := filepath.Join("/home/testuser", ".config", "godc", "config.toml")
	fs.files[configPath] = []byte("invalid toml [[[")

	_, err := app.load()
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("expected decode error, got: %v", err)
	}
}

func TestInitGetwdError(t *testing.T) {
	app, _, _, _, _ := newTestApp()

	errFS := &errorFS{err: errors.New("getwd error")}
	app.fs = errFS

	err := app.Init()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "working directory") {
		t.Errorf("expected working directory error, got: %v", err)
	}
}

func TestSetupHomeDirError(t *testing.T) {
	app, _, _, _, _ := newTestApp()

	errFS := &errorFS{err: errors.New("home dir error")}
	app.fs = errFS

	err := app.Setup()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSetupMkdirAllError(t *testing.T) {
	app, fs, _, httpClient, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	// HTTP succeeds
	url := tcURL + "/" + tcFiles[runtime.GOOS+"/"+runtime.GOARCH]
	httpClient.responses[url] = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte("content"))),
	}

	// Use error FS that fails on MkdirAll
	errFS := &errorFSMkdirAllSetup{mockFS: fs}
	app.fs = errFS

	err := app.Setup()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("expected directory error, got: %v", err)
	}
}

type errorFSMkdirAllSetup struct {
	*mockFS
	mkdirCount int
}

func (e *errorFSMkdirAllSetup) MkdirAll(path string, perm os.FileMode) error {
	e.mkdirCount++
	// Fail on the second MkdirAll (for dreamcast dir, not config dir)
	if strings.Contains(path, "dreamcast") {
		return errors.New("mkdir failed")
	}
	return nil
}

func TestSetupTarError(t *testing.T) {
	app, fs, runner, httpClient, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	url := tcURL + "/" + tcFiles[runtime.GOOS+"/"+runtime.GOARCH]
	httpClient.responses[url] = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte("content"))),
	}

	runner.errors["tar"] = errors.New("tar failed")

	err := app.Setup()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "extract") {
		t.Errorf("expected extract error, got: %v", err)
	}
}

func TestSetupGitCloneError(t *testing.T) {
	app, fs, runner, httpClient, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	url := tcURL + "/" + tcFiles[runtime.GOOS+"/"+runtime.GOARCH]
	httpClient.responses[url] = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte("content"))),
	}

	runner.errors["git"] = errors.New("git clone failed")

	err := app.Setup()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "clone") {
		t.Errorf("expected clone error, got: %v", err)
	}
}

func TestSetupMakeBuildError(t *testing.T) {
	app, fs, runner, httpClient, _ := newTestApp()
	fs.homeDir = "/home/testuser"

	url := tcURL + "/" + tcFiles[runtime.GOOS+"/"+runtime.GOARCH]
	httpClient.responses[url] = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte("content"))),
	}

	runner.errors["make"] = errors.New("make failed")

	err := app.Setup()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "build libgodc") {
		t.Errorf("expected build libgodc error, got: %v", err)
	}
}
