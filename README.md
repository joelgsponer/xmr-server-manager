# XMR Server Manager

A lightweight web-based tool to manage Cloudflare DNS records for XMR mining servers. Built with Go as a single executable that runs on Windows, macOS, and Linux.

## Features

- 🌐 Web interface for easy server management
- ✅ Activate/deactivate servers with checkboxes
- 🔄 Automatic verification after each DNS operation
- 🔁 Retry logic with exponential backoff
- 📝 Comprehensive logging system
- 🔐 Hybrid credential management (env vars, files, interactive)
- 🚀 Single binary - no dependencies required
- 🖥️ Cross-platform (Windows, macOS, Linux)
- 🏭 Production/Test environment support

## Quick Start

### 1. Download the Binary

Download the appropriate binary for your system from the releases page, or build from source:

```bash
# Clone the repository
git clone https://github.com/yourusername/xmr-server-manager.git
cd xmr-server-manager

# Build for your current platform
go build -o xmr-manager main.go

# Or build for all platforms
./build.sh
```

### 2. Set Up Credentials

The application needs Cloudflare API credentials. You can provide them in several ways:

#### Option A: Environment Variables
```bash
export CF_API_TOKEN=your_cloudflare_api_token
export CF_ZONE_ID=your_zone_id
export DNS_NAME=xmr.yourdomain.com

./xmr-manager
```

#### Option B: Configuration Files
Create `.env.test` for test environment:
```env
CF_API_TOKEN=your_test_token
CF_ZONE_ID=your_test_zone_id
DNS_NAME=xmr.jonsggi.com
```

Create `.env.production` for production:
```env
CF_API_TOKEN=your_production_token
CF_ZONE_ID=your_production_zone_id
DNS_NAME=xmr.qubic.li
```

#### Option C: Interactive Setup
Just run the application and it will prompt you for credentials:
```bash
./xmr-manager
```

### 3. Run the Application

```bash
# Run in test mode (default)
./xmr-manager

# Run in production mode
./xmr-manager -env production

# Run on a different port (default is 9876)
./xmr-manager -port 8090

# Use a custom config file
./xmr-manager -config /path/to/custom.env

# Backup operations (see Backup Management section)
./xmr-manager -backup
./xmr-manager -list-backups
./xmr-manager -restore servers.test.json.backup-20240128-150405
```

### 4. Access the Web Interface

The browser will open automatically. If not, navigate to:
```
http://localhost:9876
```

To disable automatic browser opening:
```bash
./xmr-manager -no-browser
```

## Usage

### Managing Servers

1. **First Run**: If no server configuration exists, the app will automatically import existing DNS records from Cloudflare
2. **Activate/Deactivate**: Use checkboxes to select which servers should be active
3. **Apply Changes**: Click "Update DNS Records" to apply changes
4. **Verification**: Each operation is verified and logged

### Server Configuration

The application stores server configurations in JSON files:
- `servers.test.json` - Test environment servers
- `servers.production.json` - Production environment servers

Example server configuration:
```json
{
  "environment": "test",
  "domain": "xmr.jonsggi.com",
  "last_sync": "2024-01-28T10:15:00Z",
  "servers": [
    {
      "alias": "mev01",
      "ip": "12.12.12.2",
      "description": "Mining server 1",
      "region": "us-east"
    }
  ]
}
```

### Logging

Logs are stored in the `logs/` directory with daily rotation:
- `xmr-manager-test-2024-01-28.log`
- `xmr-manager-production-2024-01-28.log`

Log entries include:
- Timestamp
- Environment (TEST/PROD)
- Log level (INFO/WARNING/ERROR/SUCCESS)
- Operation details

### Backup Management

The application includes a comprehensive backup system for server configurations:

#### Automatic Backups
- Backups are created automatically before any configuration change
- Default retention: 10 most recent backups (configurable)
- Old backups are automatically cleaned up

#### Manual Backup Commands

1. **Create a backup**:
   ```bash
   ./xmr-manager -backup
   # Creates: servers.test.json.backup-20240128-150405
   ```

2. **List available backups**:
   ```bash
   ./xmr-manager -list-backups
   
   # Output:
   # Available backups for test environment:
   # --------------------------------------------------------------------------------
   #  1. servers.test.json.backup-20240128-150405
   #     Size: 1234 bytes | Modified: 2024-01-28 15:04:05
   #     Status: KEPT (within retention limit)
   # 
   # Retention policy: Keep 10 most recent backups
   # Backup directory: .
   ```

3. **Restore from backup**:
   ```bash
   ./xmr-manager -restore servers.test.json.backup-20240128-150405
   ```

#### Backup Options

- **Custom backup directory**:
  ```bash
  ./xmr-manager -backup-dir /path/to/backups
  ```

- **Change retention policy**:
  ```bash
  # Keep 20 backups
  ./xmr-manager -keep-backups 20
  
  # Keep unlimited backups
  ./xmr-manager -keep-backups 0
  ```

- **Environment-specific operations**:
  ```bash
  # Backup production configuration
  ./xmr-manager -env production -backup
  
  # List production backups
  ./xmr-manager -env production -list-backups
  ```

#### Backup Files
- Format: `servers.{env}.json.backup-{timestamp}`
- Location: Same directory as config file (or custom backup directory)
- Content: Complete server configuration including all DNS record details

## Building from Source

### Prerequisites

- Go 1.21 or higher
- Git

### Build Instructions

```bash
# Install dependencies
go mod download

# Build for current platform
go build -o xmr-manager main.go

# Build for Windows (from macOS/Linux)
GOOS=windows GOARCH=amd64 go build -o xmr-manager.exe main.go

# Build all platforms
./build.sh
```

## API Endpoints

- `GET /` - Web interface
- `POST /api/update` - Update DNS records
- `GET /health` - Health check endpoint

## Security Considerations

- API tokens are never logged in full (only first/last 4 characters)
- Configuration files are created with restricted permissions (600)
- No authentication required as it's designed to run locally
- HTTPS not implemented - use a reverse proxy if needed

## Troubleshooting

### Common Issues

1. **"Failed to fetch DNS records"**
   - Check your API token has the correct permissions
   - Verify the Zone ID is correct
   - Ensure the DNS_NAME matches your Cloudflare setup

2. **"Rate limited"**
   - The app automatically retries with exponential backoff
   - Wait a few minutes if you see repeated rate limit errors

3. **"Verification failed"**
   - DNS propagation can take a few seconds
   - The app waits 2 seconds before verification
   - Check logs for detailed error messages

### Debug Mode

For detailed logging, set the LOG_LEVEL environment variable:
```bash
LOG_LEVEL=DEBUG ./xmr-manager
```

## Environment Variables

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| CF_API_TOKEN | Cloudflare API token | Yes | - |
| CF_ZONE_ID | Cloudflare Zone ID | Yes | - |
| DNS_NAME | Domain to manage | Yes | - |
| LOG_LEVEL | Logging level | No | INFO |

## Contributing

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the LICENSE file for details.