#!/bin/bash

# XMR Server Manager - Cross-platform build script

# Color codes for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}Building XMR Server Manager...${NC}"

# Clean previous builds
echo "Cleaning previous builds..."
rm -rf dist/
mkdir -p dist

# Get version and build time
VERSION="1.0.0"
BUILD_TIME=$(date -u +"%Y-%m-%d %H:%M:%S UTC")

# Build flags
LDFLAGS="-s -w -X main.Version=$VERSION -X 'main.BuildTime=$BUILD_TIME'"

# Windows (64-bit)
echo -e "\n${GREEN}Building for Windows (64-bit)...${NC}"
GOOS=windows GOARCH=amd64 go build -ldflags="$LDFLAGS" -o dist/xmr-manager-windows-amd64.exe main.go

# Windows (32-bit)
echo -e "\n${GREEN}Building for Windows (32-bit)...${NC}"
GOOS=windows GOARCH=386 go build -ldflags="$LDFLAGS" -o dist/xmr-manager-windows-386.exe main.go

# macOS (Intel)
echo -e "\n${GREEN}Building for macOS (Intel)...${NC}"
GOOS=darwin GOARCH=amd64 go build -ldflags="$LDFLAGS" -o dist/xmr-manager-darwin-amd64 main.go

# macOS (Apple Silicon)
echo -e "\n${GREEN}Building for macOS (Apple Silicon)...${NC}"
GOOS=darwin GOARCH=arm64 go build -ldflags="$LDFLAGS" -o dist/xmr-manager-darwin-arm64 main.go

# Linux (64-bit)
echo -e "\n${GREEN}Building for Linux (64-bit)...${NC}"
GOOS=linux GOARCH=amd64 go build -ldflags="$LDFLAGS" -o dist/xmr-manager-linux-amd64 main.go

# Linux (ARM64)
echo -e "\n${GREEN}Building for Linux (ARM64)...${NC}"
GOOS=linux GOARCH=arm64 go build -ldflags="$LDFLAGS" -o dist/xmr-manager-linux-arm64 main.go

# Create checksums
echo -e "\n${GREEN}Creating checksums...${NC}"
cd dist
shasum -a 256 * > checksums.txt
cd ..

# Show results
echo -e "\n${GREEN}Build complete!${NC}"
echo "Binaries created in dist/ directory:"
ls -lh dist/

echo -e "\n${YELLOW}File sizes:${NC}"
du -h dist/* | sort -h