# godc

CLI for building Go programs for Sega Dreamcast.

## Install

```bash
go install github.com/drpaneas/godc@latest
```

## Setup

Download and install the Dreamcast toolchain:

```bash
godc setup
```

This installs to `~/dreamcast` and updates your shell config.

## Usage

```bash
# Initialize a project
godc init

# Build
godc build

# Build to specific output
godc build -o game.elf

# Run in emulator
godc run

# Run on hardware via dc-tool-ip
godc run --ip

# Check installation
godc doctor

# Configure paths/emulator/IP
godc config

# Update libgodc
godc update

# Show environment info
godc env
```

## Configuration

Config file: `~/.config/godc/config.toml`

```toml
Path = "/home/user/dreamcast"
Emu = "flycast"
IP = "192.168.2.203"
```

## Platforms

- macOS (arm64, amd64)
- Linux (amd64)

