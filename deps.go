// godc - Dependency interfaces for testability
package main

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// FileSystem abstracts filesystem operations for testing
type FileSystem interface {
	Stat(name string) (os.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
	Create(name string) (io.WriteCloser, error)
	Open(name string) (io.ReadCloser, error)
	OpenFile(name string, flag int, perm os.FileMode) (io.WriteCloser, error)
	ReadDir(name string) ([]os.DirEntry, error)
	Remove(name string) error
	RemoveAll(path string) error
	MkdirTemp(dir, pattern string) (string, error)
	TempDir() string
	UserHomeDir() (string, error)
	Getwd() (string, error)
	Chmod(name string, mode os.FileMode) error
	Symlink(oldname, newname string) error
}

// CommandRunner abstracts command execution for testing
type CommandRunner interface {
	Run(name string, args []string, dir string, env []string, stdout, stderr io.Writer) error
	Output(name string, args ...string) ([]byte, error)
}

// HTTPClient abstracts HTTP operations for testing
type HTTPClient interface {
	Get(url string) (*http.Response, error)
}

// --- Real implementations ---

// RealFS implements FileSystem using the real filesystem
type RealFS struct{}

func (RealFS) Stat(name string) (os.FileInfo, error) {
	return os.Stat(name)
}

func (RealFS) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (RealFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

func (RealFS) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (RealFS) Create(name string) (io.WriteCloser, error) {
	return os.Create(name)
}

func (RealFS) Open(name string) (io.ReadCloser, error) {
	return os.Open(name)
}

func (RealFS) OpenFile(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
	return os.OpenFile(name, flag, perm)
}

func (RealFS) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(name)
}

func (RealFS) Remove(name string) error {
	return os.Remove(name)
}

func (RealFS) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

func (RealFS) MkdirTemp(dir, pattern string) (string, error) {
	return os.MkdirTemp(dir, pattern)
}

func (RealFS) UserHomeDir() (string, error) {
	return os.UserHomeDir()
}

func (RealFS) Getwd() (string, error) {
	return os.Getwd()
}

func (RealFS) TempDir() string {
	return os.TempDir()
}

func (RealFS) Chmod(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}

func (RealFS) Symlink(oldname, newname string) error {
	return os.Symlink(oldname, newname)
}

// RealRunner implements CommandRunner using real exec
type RealRunner struct{}

func (RealRunner) Run(name string, args []string, dir string, env []string, stdout, stderr io.Writer) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Dir = dir
	cmd.Env = env
	return cmd.Run()
}

func (RealRunner) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// RealHTTP implements HTTPClient using real http.Client
type RealHTTP struct {
	Timeout time.Duration
}

func (r RealHTTP) Get(url string) (*http.Response, error) {
	client := &http.Client{Timeout: r.Timeout}
	return client.Get(url)
}
