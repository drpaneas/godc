# GODC - Go Dreamcast Command Line Tool

<p align="center">
  <img src="godc.png" alt="godc logo" width="300">
</p>

<p align="center">
  <a href="https://github.com/drpaneas/godc/actions/workflows/lint.yml"><img src="https://github.com/drpaneas/godc/actions/workflows/lint.yml/badge.svg" alt="Lint"></a>
  <a href="https://goreportcard.com/report/github.com/drpaneas/godc"><img src="https://goreportcard.com/badge/github.com/drpaneas/godc" alt="Go Report Card"></a>
  <a href="https://pkg.go.dev/github.com/drpaneas/godc"><img src="https://pkg.go.dev/badge/github.com/drpaneas/godc.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/drpaneas/godc" alt="License"></a>
</p>

CLI for building Go programs for Sega Dreamcast.
It uses [libgodc](https://drpaneas.github.io/libgodc/), a minimal Go runtime for the Dreamcast, along with [pre-built KOS](https://github.com/drpaneas/dreamcast-toolchain-builds) ready for coding.

## Install

```bash
# First make sure you have Go properly installed
go install github.com/drpaneas/godc@latest
```

> **Note:** Make sure `$GOBIN` (or `$GOPATH/bin`) is in your `PATH` so that `godc` is available after installation.

## Setup

Download and install the Dreamcast toolchain:

```bash
godc setup  # Download and install the libraries
godc doctor # Check installation (optional)
godc env    # Show environment info (optional)
```

This installs to `~/dreamcast` and updates your shell config.

```shell
KallistiOS environment loaded:
  KOS_BASE:    $HOME/dreamcast/kos
  KOS_CC_BASE: $HOME/dreamcast/sh-elf
  KOS_PORTS:   $HOME/dreamcast/kos-ports
  GCC version: 15.1.0
```

## Usage

```bash
mkdir my_game; cd my_game # Create a directory to work
godc init     # Initialize it
vim main.go   # Write you code
godc build    # Build
godc run      # Run in emulator
godc run --ip # Run on hardware via dc-tool-ip
```

## Custom Configuration (optional)

```bash
godc config # Configure emulators, IP Addresses etc
```

Config file: `~/.config/godc/config.toml`

```toml
Path = "/home/user/dreamcast"
Emu = "flycast"
IP = "192.168.2.203"
```
