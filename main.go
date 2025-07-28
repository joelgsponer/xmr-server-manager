package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// Version information
var (
	Version   = "1.1.0"
	BuildTime = "dev"
)

// Configuration structures
type Server struct {
	// Custom fields (persistent)
	Alias           string `json:"alias"`
	Description     string `json:"description"`
	Region          string `json:"region,omitempty"`
	FirstSeenOn     string `json:"first_seen_on,omitempty"`     // When we first discovered this server
	LastActivatedOn string `json:"last_activated_on,omitempty"` // When it was last activated
	
	// Cloudflare DNS record fields (configuration)
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Content  string   `json:"content"` // This is the IP address
	TTL      int      `json:"ttl"`
	Proxied  bool     `json:"proxied"`
	Comment  string   `json:"comment,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	
	// Cloudflare record state (transient - not saved)
	ID         string `json:"-"` // Current record ID, changes on each create/delete
	CreatedOn  string `json:"-"` // From Cloudflare, changes each time
	ModifiedOn string `json:"-"` // From Cloudflare, changes each time
	Proxiable  bool   `json:"-"` // From Cloudflare
}

type ServerConfig struct {
	Environment string    `json:"environment"`
	Domain      string    `json:"domain"`
	LastSync    time.Time `json:"last_sync"`
	Servers     []Server  `json:"servers"`
}

type Credentials struct {
	Token  string
	ZoneID string
	Domain string
}

type CloudflareRecord struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Name       string   `json:"name"`
	Content    string   `json:"content"`
	TTL        int      `json:"ttl"`
	Proxied    bool     `json:"proxied"`
	Comment    string   `json:"comment,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	CreatedOn  string   `json:"created_on,omitempty"`
	ModifiedOn string   `json:"modified_on,omitempty"`
	Proxiable  bool     `json:"proxiable,omitempty"`
}

type CloudflareResponse struct {
	Success bool               `json:"success"`
	Errors  []interface{}      `json:"errors"`
	Result  []CloudflareRecord `json:"result"`
}

type CloudflareCreateResponse struct {
	Success bool             `json:"success"`
	Errors  []interface{}    `json:"errors"`
	Result  CloudflareRecord `json:"result"`
}

// Global variables
var (
	environment = flag.String("env", "test", "Environment (test/production)")
	port        = flag.Int("port", 9876, "Port to run the server on")
	configFile  = flag.String("config", "", "Path to custom env file")
	noBrowser   = flag.Bool("no-browser", false, "Don't open browser automatically")
	
	// Backup related flags
	backup      = flag.Bool("backup", false, "Create a backup of the current configuration")
	restore     = flag.String("restore", "", "Restore configuration from a backup file")
	listBackups = flag.Bool("list-backups", false, "List available backup files")
	backupDir   = flag.String("backup-dir", "", "Directory to store backups (default: same as config)")
	keepBackups = flag.Int("keep-backups", 10, "Number of backups to keep (0 = unlimited)")
	
	logger      *Logger
	credentials *Credentials
	configMutex sync.RWMutex
)

// Logger structure
type Logger struct {
	file *os.File
	mu   sync.Mutex
}

func NewLogger(env string) (*Logger, error) {
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}
	
	timestamp := time.Now().Format("2006-01-02")
	filename := filepath.Join(logDir, fmt.Sprintf("xmr-manager-%s-%s.log", env, timestamp))
	
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	
	return &Logger{file: file}, nil
}

func (l *Logger) Log(level, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	env := strings.ToUpper(*environment)
	logEntry := fmt.Sprintf("[%s] [%s] [%s] %s\n", timestamp, env, level, message)
	
	fmt.Print(logEntry)
	if l.file != nil {
		l.file.WriteString(logEntry)
	}
}

func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}

// Embedded HTML template
const indexHTML = `<!DOCTYPE html>
<html>
<head>
    <title>XMR Server Manager - {{.Environment}}</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            max-width: 1200px;
            margin: 0 auto;
            padding: 20px;
            background-color: #f5f5f5;
        }
        .header {
            background-color: {{if eq .Environment "production"}}#dc3545{{else}}#fd7e14{{end}};
            color: white;
            padding: 15px;
            border-radius: 5px;
            margin-bottom: 20px;
        }
        .header h1 {
            margin: 0;
        }
        .info {
            background-color: white;
            padding: 15px;
            border-radius: 5px;
            margin-bottom: 20px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        table {
            width: 100%;
            background-color: white;
            border-radius: 5px;
            overflow: hidden;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        th, td {
            padding: 12px;
            text-align: left;
            border-bottom: 1px solid #ddd;
        }
        th {
            background-color: #f8f9fa;
            font-weight: bold;
        }
        tr:hover {
            background-color: #f5f5f5;
        }
        .checkbox {
            width: 20px;
            height: 20px;
            cursor: pointer;
        }
        .button {
            background-color: {{if eq .Environment "production"}}#dc3545{{else}}#fd7e14{{end}};
            color: white;
            border: none;
            padding: 10px 20px;
            border-radius: 5px;
            cursor: pointer;
            font-size: 16px;
            margin-top: 20px;
        }
        .button:hover {
            opacity: 0.9;
        }
        .status {
            margin-top: 20px;
            padding: 15px;
            border-radius: 5px;
            display: none;
        }
        .status.success {
            background-color: #d4edda;
            color: #155724;
            border: 1px solid #c3e6cb;
        }
        .status.error {
            background-color: #f8d7da;
            color: #721c24;
            border: 1px solid #f5c6cb;
        }
        .status.info {
            background-color: #d1ecf1;
            color: #0c5460;
            border: 1px solid #bee5eb;
        }
        .spinner {
            display: inline-block;
            width: 20px;
            height: 20px;
            border: 3px solid #f3f3f3;
            border-top: 3px solid {{if eq .Environment "production"}}#dc3545{{else}}#fd7e14{{end}};
            border-radius: 50%;
            animation: spin 1s linear infinite;
            margin-right: 10px;
            vertical-align: middle;
        }
        @keyframes spin {
            0% { transform: rotate(0deg); }
            100% { transform: rotate(360deg); }
        }
        .log-entry {
            font-family: monospace;
            font-size: 12px;
            margin: 2px 0;
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>XMR Server Manager - {{.Environment | toUpper}} Environment</h1>
        <p>Domain: {{.Domain}}</p>
    </div>
    
    <div class="info">
        <strong>Total Servers:</strong> {{len .Servers}} | 
        <strong>Active:</strong> {{.ActiveCount}} | 
        <strong>Inactive:</strong> {{.InactiveCount}} |
        <strong>Last Updated:</strong> <span id="lastUpdate">{{.LastUpdate}}</span>
    </div>
    
    <form id="serverForm" onsubmit="updateServers(event)">
        <table>
            <thead>
                <tr>
                    <th>Alias</th>
                    <th>IP Address</th>
                    <th>Description</th>
                    <th>TTL</th>
                    <th>Proxy</th>
                    <th>Status</th>
                    <th>Active</th>
                </tr>
            </thead>
            <tbody>
                {{range .Servers}}
                <tr>
                    <td><strong>{{.Server.Alias}}</strong></td>
                    <td>{{.Server.Content}}</td>
                    <td>{{.Server.Description}}</td>
                    <td>{{.Server.TTL}}s</td>
                    <td>
                        {{if .Server.Proxied}}
                            <span style="color: orange;" title="Proxied through Cloudflare">🛡️ On</span>
                        {{else}}
                            <span style="color: gray;" title="DNS only">☁️ Off</span>
                        {{end}}
                    </td>
                    <td>
                        {{if .IsActive}}
                            <span style="color: green;">● Active</span>
                        {{else}}
                            <span style="color: gray;">○ Inactive</span>
                        {{end}}
                    </td>
                    <td>
                        <input type="checkbox" class="checkbox" 
                               name="active" 
                               value="{{.Server.Content}}" 
                               data-alias="{{.Server.Alias}}"
                               data-proxied="{{.Server.Proxied}}"
                               data-ttl="{{.Server.TTL}}"
                               {{if .IsActive}}checked{{end}}>
                    </td>
                </tr>
                {{end}}
            </tbody>
        </table>
        
        <button type="submit" class="button" {{if eq .Environment "production"}}onclick="return confirmProduction()"{{end}}>
            Update DNS Records
        </button>
    </form>
    
    <div id="status" class="status"></div>
    
    <div id="operationLog" style="margin-top: 20px; display: none;">
        <h3>Operation Log:</h3>
        <div id="logEntries" style="background-color: white; padding: 10px; border-radius: 5px; max-height: 300px; overflow-y: auto;"></div>
    </div>
    
    <script>
        function confirmProduction() {
            return confirm('⚠️ WARNING: You are about to modify PRODUCTION DNS records. Are you sure?');
        }
        
        function updateServers(event) {
            event.preventDefault();
            
            const statusDiv = document.getElementById('status');
            const logDiv = document.getElementById('operationLog');
            const logEntries = document.getElementById('logEntries');
            
            statusDiv.className = 'status info';
            statusDiv.innerHTML = '<span class="spinner"></span>Processing DNS updates...';
            statusDiv.style.display = 'block';
            logDiv.style.display = 'block';
            logEntries.innerHTML = '';
            
            const formData = new FormData(event.target);
            const activeIPs = formData.getAll('active');
            
            // Build request data with aliases, proxy status, and TTL
            const servers = [];
            document.querySelectorAll('input[name="active"]').forEach(checkbox => {
                if (activeIPs.includes(checkbox.value)) {
                    servers.push({
                        ip: checkbox.value,
                        alias: checkbox.dataset.alias,
                        proxied: checkbox.dataset.proxied === 'true',
                        ttl: parseInt(checkbox.dataset.ttl) || 60,
                        active: true
                    });
                }
            });
            
            // Add log entry
            function addLog(message, type = 'info') {
                const entry = document.createElement('div');
                entry.className = 'log-entry';
                const timestamp = new Date().toLocaleTimeString();
                entry.innerHTML = '[' + timestamp + '] ' + message;
                entry.style.color = type === 'error' ? '#dc3545' : type === 'success' ? '#28a745' : '#333';
                logEntries.appendChild(entry);
                logEntries.scrollTop = logEntries.scrollHeight;
            }
            
            addLog('Starting DNS update process...');
            
            fetch('/api/update', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({active_servers: servers})
            })
            .then(response => response.json())
            .then(data => {
                if (data.success) {
                    statusDiv.className = 'status success';
                    statusDiv.innerHTML = '✅ ' + data.message;
                    
                    // Log details
                    if (data.details) {
                        data.details.forEach(detail => {
                            addLog(detail.message, detail.status);
                        });
                    }
                    
                    // Reload page after 3 seconds
                    setTimeout(() => {
                        window.location.reload();
                    }, 3000);
                } else {
                    statusDiv.className = 'status error';
                    statusDiv.innerHTML = '❌ Error: ' + data.message;
                    addLog('Error: ' + data.message, 'error');
                }
            })
            .catch(error => {
                statusDiv.className = 'status error';
                statusDiv.innerHTML = '❌ Error: ' + error.message;
                addLog('Fatal error: ' + error.message, 'error');
            });
        }
        
        // Auto-refresh every 30 seconds
        setInterval(() => {
            document.getElementById('lastUpdate').textContent = new Date().toLocaleString();
        }, 30000);
    </script>
</body>
</html>`

// CloudflareClient handles all API interactions
type CloudflareClient struct {
	credentials *Credentials
	httpClient  *http.Client
}

func NewCloudflareClient(creds *Credentials) *CloudflareClient {
	return &CloudflareClient{
		credentials: creds,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *CloudflareClient) makeRequest(method, endpoint string, body io.Reader) (*http.Request, error) {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s%s", c.credentials.ZoneID, endpoint)
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	
	req.Header.Set("Authorization", "Bearer "+c.credentials.Token)
	req.Header.Set("Content-Type", "application/json")
	
	return req, nil
}

func (c *CloudflareClient) GetDNSRecords() ([]CloudflareRecord, error) {
	req, err := c.makeRequest("GET", fmt.Sprintf("/dns_records?type=A&name=%s", c.credentials.Domain), nil)
	if err != nil {
		return nil, err
	}
	
	logger.Log("INFO", fmt.Sprintf("Fetching DNS records for %s", c.credentials.Domain))
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to fetch DNS records: %v", err))
		return nil, err
	}
	defer resp.Body.Close()
	
	var cfResp CloudflareResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return nil, err
	}
	
	if !cfResp.Success {
		return nil, fmt.Errorf("cloudflare API error: %v", cfResp.Errors)
	}
	
	logger.Log("INFO", fmt.Sprintf("Found %d DNS records", len(cfResp.Result)))
	return cfResp.Result, nil
}

func (c *CloudflareClient) CreateDNSRecord(ip, alias string, proxied bool, ttl int) (string, error) {
	if ttl <= 0 {
		ttl = 60 // Default to 1 minute
	}
	
	payload := map[string]interface{}{
		"type":    "A",
		"name":    c.credentials.Domain,
		"content": ip,
		"ttl":     ttl,
		"proxied": proxied,
		"comment": alias,
	}
	
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	
	req, err := c.makeRequest("POST", "/dns_records", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	
	logger.Log("INFO", fmt.Sprintf("Creating DNS record for %s (%s)", alias, ip))
	
	// Retry logic with exponential backoff
	var resp *http.Response
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt)) * time.Second
			logger.Log("INFO", fmt.Sprintf("Retrying after %v (attempt %d/%d)", delay, attempt+1, maxRetries))
			time.Sleep(delay)
		}
		
		resp, err = c.httpClient.Do(req)
		if err == nil && resp.StatusCode != 429 {
			break
		}
		
		if resp != nil {
			resp.Body.Close()
		}
	}
	
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to create DNS record after %d attempts: %v", maxRetries, err))
		return "", err
	}
	defer resp.Body.Close()
	
	var cfResp CloudflareCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return "", err
	}
	
	if !cfResp.Success {
		logger.Log("ERROR", fmt.Sprintf("Cloudflare API error: %v", cfResp.Errors))
		return "", fmt.Errorf("cloudflare API error: %v", cfResp.Errors)
	}
	
	// Verify creation
	recordID := cfResp.Result.ID
	logger.Log("INFO", fmt.Sprintf("Created record with ID: %s, verifying...", recordID))
	
	time.Sleep(2 * time.Second)
	
	if verified := c.VerifyRecord(recordID, ip); verified {
		logger.Log("SUCCESS", fmt.Sprintf("DNS record for %s (%s) created and verified", alias, ip))
		return recordID, nil
	}
	
	logger.Log("WARNING", "Record created but verification failed")
	return recordID, nil
}

func (c *CloudflareClient) DeleteDNSRecord(recordID string) error {
	req, err := c.makeRequest("DELETE", fmt.Sprintf("/dns_records/%s", recordID), nil)
	if err != nil {
		return err
	}
	
	logger.Log("INFO", fmt.Sprintf("Deleting DNS record %s", recordID))
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to delete DNS record: %v", err))
		return err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 {
		return fmt.Errorf("failed to delete record: status %d", resp.StatusCode)
	}
	
	// Verify deletion
	time.Sleep(2 * time.Second)
	records, err := c.GetDNSRecords()
	if err == nil {
		for _, record := range records {
			if record.ID == recordID {
				logger.Log("WARNING", "Record deletion not yet propagated")
				return nil
			}
		}
	}
	
	logger.Log("SUCCESS", fmt.Sprintf("DNS record %s deleted and verified", recordID))
	return nil
}

func (c *CloudflareClient) VerifyRecord(recordID, expectedIP string) bool {
	records, err := c.GetDNSRecords()
	if err != nil {
		return false
	}
	
	for _, record := range records {
		if record.ID == recordID && record.Content == expectedIP {
			return true
		}
	}
	
	return false
}

// Credential management
func getCredentials(env string) (*Credentials, error) {
	creds := &Credentials{}
	
	// 1. Try command-line config file
	if *configFile != "" {
		logger.Log("INFO", fmt.Sprintf("Loading credentials from custom file: %s", *configFile))
		if err := godotenv.Load(*configFile); err == nil {
			creds.Token = os.Getenv("CF_API_TOKEN")
			creds.ZoneID = os.Getenv("CF_ZONE_ID")
			creds.Domain = os.Getenv("DNS_NAME")
			if creds.Token != "" && creds.ZoneID != "" && creds.Domain != "" {
				return creds, nil
			}
		}
	}
	
	// 2. Try environment variables
	if token := os.Getenv("CF_API_TOKEN"); token != "" {
		logger.Log("INFO", "Loading credentials from environment variables")
		creds.Token = token
		creds.ZoneID = os.Getenv("CF_ZONE_ID")
		creds.Domain = os.Getenv("DNS_NAME")
		if creds.Token != "" && creds.ZoneID != "" && creds.Domain != "" {
			return creds, nil
		}
	}
	
	// 3. Try .env file
	envFile := fmt.Sprintf(".env.%s", env)
	if _, err := os.Stat(envFile); err == nil {
		logger.Log("INFO", fmt.Sprintf("Loading credentials from %s", envFile))
		if err := godotenv.Load(envFile); err == nil {
			creds.Token = os.Getenv("CF_API_TOKEN")
			creds.ZoneID = os.Getenv("CF_ZONE_ID")
			creds.Domain = os.Getenv("DNS_NAME")
			if creds.Token != "" && creds.ZoneID != "" && creds.Domain != "" {
				return creds, nil
			}
		}
	}
	
	// 4. Interactive setup
	logger.Log("INFO", "No credentials found, starting interactive setup")
	return interactiveSetup(env)
}

func interactiveSetup(env string) (*Credentials, error) {
	reader := bufio.NewReader(os.Stdin)
	creds := &Credentials{}
	
	fmt.Printf("\n=== Cloudflare Credentials Setup for %s ===\n", strings.ToUpper(env))
	fmt.Println("You can find these values in your Cloudflare dashboard")
	fmt.Println("API Token: https://dash.cloudflare.com/profile/api-tokens")
	fmt.Println("Zone ID: Domain Overview page -> API section\n")
	
	fmt.Print("Enter Cloudflare API Token: ")
	token, _ := reader.ReadString('\n')
	creds.Token = strings.TrimSpace(token)
	
	fmt.Print("Enter Zone ID: ")
	zoneID, _ := reader.ReadString('\n')
	creds.ZoneID = strings.TrimSpace(zoneID)
	
	fmt.Print("Enter DNS Name (e.g., xmr.qubic.li): ")
	domain, _ := reader.ReadString('\n')
	creds.Domain = strings.TrimSpace(domain)
	
	// Save to .env file
	envFile := fmt.Sprintf(".env.%s", env)
	fmt.Printf("\nSave these credentials to %s? (y/n): ", envFile)
	save, _ := reader.ReadString('\n')
	
	if strings.ToLower(strings.TrimSpace(save)) == "y" {
		content := fmt.Sprintf("CF_API_TOKEN=%s\nCF_ZONE_ID=%s\nDNS_NAME=%s\n", 
			creds.Token, creds.ZoneID, creds.Domain)
		
		if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to save credentials: %v", err))
		} else {
			logger.Log("INFO", fmt.Sprintf("Credentials saved to %s", envFile))
		}
	}
	
	return creds, nil
}

// Server configuration management
func loadServerConfig(env string) (*ServerConfig, error) {
	configFile := fmt.Sprintf("servers.%s.json", env)
	
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Log("INFO", fmt.Sprintf("No server configuration found at %s", configFile))
			return nil, nil
		}
		return nil, err
	}
	
	var config ServerConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	
	logger.Log("INFO", fmt.Sprintf("Loaded %d servers from %s", len(config.Servers), configFile))
	return &config, nil
}

// Backup management functions
func getBackupDir(configFile string) string {
	if *backupDir != "" {
		return *backupDir
	}
	return filepath.Dir(configFile)
}

func getBackupFiles(env string) ([]string, error) {
	configFile := fmt.Sprintf("servers.%s.json", env)
	dir := getBackupDir(configFile)
	pattern := fmt.Sprintf("%s.backup-*", filepath.Base(configFile))
	
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, err
	}
	
	// Sort by modification time (newest first)
	sort.Slice(files, func(i, j int) bool {
		fi, _ := os.Stat(files[i])
		fj, _ := os.Stat(files[j])
		return fi.ModTime().After(fj.ModTime())
	})
	
	return files, nil
}

func cleanOldBackups(env string) error {
	if *keepBackups <= 0 {
		return nil // Unlimited backups
	}
	
	backups, err := getBackupFiles(env)
	if err != nil {
		return err
	}
	
	if len(backups) <= *keepBackups {
		return nil
	}
	
	// Remove old backups
	for i := *keepBackups; i < len(backups); i++ {
		if err := os.Remove(backups[i]); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to remove old backup %s: %v", backups[i], err))
		} else {
			logger.Log("INFO", fmt.Sprintf("Removed old backup: %s", filepath.Base(backups[i])))
		}
	}
	
	return nil
}

func createBackup(env string) (string, error) {
	configFile := fmt.Sprintf("servers.%s.json", env)
	
	// Check if config exists
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no configuration file to backup: %s", configFile)
		}
		return "", err
	}
	
	// Create backup filename
	dir := getBackupDir(configFile)
	backupFile := filepath.Join(dir, fmt.Sprintf("%s.backup-%s", filepath.Base(configFile), time.Now().Format("20060102-150405")))
	
	// Ensure backup directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	
	// Write backup
	if err := os.WriteFile(backupFile, data, 0644); err != nil {
		return "", err
	}
	
	logger.Log("INFO", fmt.Sprintf("Backup created: %s", backupFile))
	
	// Clean old backups
	cleanOldBackups(env)
	
	return backupFile, nil
}

func restoreBackup(backupFile string, env string) error {
	// Read backup file
	data, err := os.ReadFile(backupFile)
	if err != nil {
		return fmt.Errorf("failed to read backup file: %v", err)
	}
	
	// Validate it's valid JSON
	var config ServerConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("invalid backup file format: %v", err)
	}
	
	// Create a backup of current config before restoring
	configFile := fmt.Sprintf("servers.%s.json", env)
	if _, err := os.Stat(configFile); err == nil {
		if _, err := createBackup(env); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to backup current config: %v", err))
		}
	}
	
	// Restore the backup
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		return fmt.Errorf("failed to restore backup: %v", err)
	}
	
	logger.Log("INFO", fmt.Sprintf("Configuration restored from %s", backupFile))
	return nil
}

func listBackupFiles(env string) error {
	backups, err := getBackupFiles(env)
	if err != nil {
		return err
	}
	
	if len(backups) == 0 {
		fmt.Printf("No backups found for %s environment\n", env)
		return nil
	}
	
	fmt.Printf("\nAvailable backups for %s environment:\n", env)
	fmt.Println(strings.Repeat("-", 80))
	
	for i, backup := range backups {
		info, err := os.Stat(backup)
		if err != nil {
			continue
		}
		
		// Extract timestamp from filename
		base := filepath.Base(backup)
		
		fmt.Printf("%2d. %s\n", i+1, base)
		fmt.Printf("    Size: %d bytes | Modified: %s\n", 
			info.Size(), 
			info.ModTime().Format("2006-01-02 15:04:05"))
		
		if i < *keepBackups {
			fmt.Printf("    Status: KEPT (within retention limit)\n")
		} else {
			fmt.Printf("    Status: TO BE REMOVED (exceeds retention limit)\n")
		}
		fmt.Println()
	}
	
	fmt.Printf("Retention policy: Keep %d most recent backups\n", *keepBackups)
	fmt.Printf("Backup directory: %s\n", getBackupDir(fmt.Sprintf("servers.%s.json", env)))
	
	return nil
}

func saveServerConfig(env string, config *ServerConfig) error {
	configFile := fmt.Sprintf("servers.%s.json", env)
	
	// Create backup if file exists
	if _, err := os.Stat(configFile); err == nil {
		if _, err := createBackup(env); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to create backup: %v", err))
		}
	}
	
	config.LastSync = time.Now()
	
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		return err
	}
	
	logger.Log("INFO", fmt.Sprintf("Server configuration saved to %s", configFile))
	return nil
}

func importFromCloudflare(env string, records []CloudflareRecord) (*ServerConfig, error) {
	config := &ServerConfig{
		Environment: env,
		Domain:      credentials.Domain,
		Servers:     []Server{},
	}
	
	for _, record := range records {
		// Use comment as alias, or generate one
		alias := record.Comment
		if alias == "" {
			alias = fmt.Sprintf("server-%s", strings.ReplaceAll(record.Content, ".", "-"))
		}
		
		now := time.Now().Format(time.RFC3339)
		server := Server{
			// Custom fields
			Alias:           alias,
			Description:     fmt.Sprintf("Imported from Cloudflare on %s", time.Now().Format("2006-01-02")),
			FirstSeenOn:     now,
			LastActivatedOn: now, // It's active when we import it
			
			// Cloudflare configuration
			Type:    record.Type,
			Name:    record.Name,
			Content: record.Content,
			TTL:     record.TTL,
			Proxied: record.Proxied,
			Comment: record.Comment,
			Tags:    record.Tags,
			
			// Transient fields (set but not saved)
			ID:         record.ID,
			CreatedOn:  record.CreatedOn,
			ModifiedOn: record.ModifiedOn,
			Proxiable:  record.Proxiable,
		}
		
		config.Servers = append(config.Servers, server)
	}
	
	return config, nil
}

// Web handlers
func indexHandler(w http.ResponseWriter, r *http.Request) {
	cfClient := NewCloudflareClient(credentials)
	
	// Load server configuration
	config, err := loadServerConfig(*environment)
	if err != nil {
		http.Error(w, "Failed to load configuration", http.StatusInternalServerError)
		return
	}
	
	// Get current DNS records
	records, err := cfClient.GetDNSRecords()
	if err != nil {
		http.Error(w, "Failed to fetch DNS records", http.StatusInternalServerError)
		return
	}
	
	// If no config exists, offer to import
	if config == nil {
		if len(records) > 0 {
			// Auto-import from Cloudflare
			config, err = importFromCloudflare(*environment, records)
			if err != nil {
				http.Error(w, "Failed to import configuration", http.StatusInternalServerError)
				return
			}
			
			if err := saveServerConfig(*environment, config); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to save imported config: %v", err))
			}
		} else {
			// Create empty config
			config = &ServerConfig{
				Environment: *environment,
				Domain:      credentials.Domain,
				Servers:     []Server{},
			}
		}
	}
	
	// Build active IP map
	activeIPs := make(map[string]CloudflareRecord)
	for _, record := range records {
		activeIPs[record.Content] = record
	}
	
	// Prepare template data
	type ServerDisplay struct {
		Server   Server
		IsActive bool
		RecordID string
	}
	
	var servers []ServerDisplay
	activeCount := 0
	
	for _, server := range config.Servers {
		record, isActive := activeIPs[server.Content]
		if isActive {
			activeCount++
			// Update server with latest info from Cloudflare
			server.ID = record.ID
			server.ModifiedOn = record.ModifiedOn
		}
		
		servers = append(servers, ServerDisplay{
			Server:   server,
			IsActive: isActive,
			RecordID: record.ID,
		})
	}
	
	data := map[string]interface{}{
		"Environment":   *environment,
		"Domain":        config.Domain,
		"Servers":       servers,
		"ActiveCount":   activeCount,
		"InactiveCount": len(servers) - activeCount,
		"LastUpdate":    time.Now().Format("2006-01-02 15:04:05"),
	}
	
	tmpl := template.Must(template.New("index").Funcs(template.FuncMap{
		"toUpper": strings.ToUpper,
	}).Parse(indexHTML))
	
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

type UpdateRequest struct {
	ActiveServers []struct {
		IP      string `json:"ip"`
		Alias   string `json:"alias"`
		Proxied bool   `json:"proxied"`
		TTL     int    `json:"ttl"`
		Active  bool   `json:"active"`
	} `json:"active_servers"`
}

type UpdateResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Details []struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"details"`
}

func updateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	
	cfClient := NewCloudflareClient(credentials)
	response := UpdateResponse{Success: true}
	
	// Get current DNS records
	records, err := cfClient.GetDNSRecords()
	if err != nil {
		response.Success = false
		response.Message = fmt.Sprintf("Failed to fetch current records: %v", err)
		json.NewEncoder(w).Encode(response)
		return
	}
	
	// Build maps for comparison
	currentRecords := make(map[string]CloudflareRecord)
	for _, record := range records {
		currentRecords[record.Content] = record
	}
	
	type serverInfo struct {
		alias   string
		proxied bool
		ttl     int
	}
	requestedServers := make(map[string]serverInfo)
	for _, server := range req.ActiveServers {
		requestedServers[server.IP] = serverInfo{
			alias:   server.Alias,
			proxied: server.Proxied,
			ttl:     server.TTL,
		}
	}
	
	// Load server config to get all servers
	config, err := loadServerConfig(*environment)
	if err != nil || config == nil {
		response.Success = false
		response.Message = "Failed to load server configuration"
		json.NewEncoder(w).Encode(response)
		return
	}
	
	// Process changes
	changes := 0
	
	// Add new records
	for _, server := range config.Servers {
		if info, shouldBeActive := requestedServers[server.Content]; shouldBeActive {
			if _, exists := currentRecords[server.Content]; !exists {
				detail := struct {
					Message string `json:"message"`
					Status  string `json:"status"`
				}{}
				
				_, err := cfClient.CreateDNSRecord(server.Content, info.alias, info.proxied, info.ttl)
				if err != nil {
					detail.Message = fmt.Sprintf("Failed to activate %s (%s): %v", info.alias, server.Content, err)
					detail.Status = "error"
				} else {
					proxyStatus := "DNS-only"
					if info.proxied {
						proxyStatus = "proxied"
					}
					detail.Message = fmt.Sprintf("✓ Activated %s (%s) [%s, TTL: %d]", info.alias, server.Content, proxyStatus, info.ttl)
					detail.Status = "success"
					changes++
					
					// Update last activated timestamp
					for i := range config.Servers {
						if config.Servers[i].Content == server.Content {
							config.Servers[i].LastActivatedOn = time.Now().Format(time.RFC3339)
							break
						}
					}
				}
				
				response.Details = append(response.Details, detail)
			}
		}
	}
	
	// Remove records
	for ip, record := range currentRecords {
		if _, shouldBeActive := requestedServers[ip]; !shouldBeActive {
			detail := struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			}{}
			
			err := cfClient.DeleteDNSRecord(record.ID)
			if err != nil {
				detail.Message = fmt.Sprintf("Failed to deactivate %s (%s): %v", record.Comment, ip, err)
				detail.Status = "error"
			} else {
				detail.Message = fmt.Sprintf("✓ Deactivated %s (%s)", record.Comment, ip)
				detail.Status = "success"
				changes++
			}
			
			response.Details = append(response.Details, detail)
		}
	}
	
	if changes == 0 {
		response.Message = "No changes required"
	} else {
		response.Message = fmt.Sprintf("Successfully updated %d DNS records", changes)
		
		// Save updated configuration with new timestamps
		if err := saveServerConfig(*environment, config); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to save config after updates: %v", err))
		}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	health := map[string]interface{}{
		"status":      "healthy",
		"version":     Version,
		"build_time":  BuildTime,
		"environment": *environment,
		"uptime":      time.Since(startTime).String(),
	}
	
	// Test Cloudflare connection
	cfClient := NewCloudflareClient(credentials)
	_, err := cfClient.GetDNSRecords()
	health["cloudflare_connected"] = err == nil
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

var startTime = time.Now()

// openBrowser opens the default browser to the specified URL
func openBrowser(url string) error {
	var err error
	
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to open browser: %v", err))
	} else {
		logger.Log("INFO", fmt.Sprintf("Opening browser at %s", url))
	}
	
	return err
}

func main() {
	flag.Parse()
	
	// Initialize logger
	var err error
	logger, err = NewLogger(*environment)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()
	
	logger.Log("INFO", fmt.Sprintf("Starting XMR Server Manager v%s (built: %s)", Version, BuildTime))
	logger.Log("INFO", fmt.Sprintf("Environment: %s", *environment))
	logger.Log("INFO", fmt.Sprintf("Platform: %s/%s", runtime.GOOS, runtime.GOARCH))
	
	// Handle backup-related commands first
	if *listBackups {
		if err := listBackupFiles(*environment); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to list backups: %v", err))
			os.Exit(1)
		}
		os.Exit(0)
	}
	
	if *backup {
		backupFile, err := createBackup(*environment)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to create backup: %v", err))
			os.Exit(1)
		}
		fmt.Printf("Backup created: %s\n", backupFile)
		os.Exit(0)
	}
	
	if *restore != "" {
		if err := restoreBackup(*restore, *environment); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to restore backup: %v", err))
			os.Exit(1)
		}
		fmt.Printf("Configuration restored from: %s\n", *restore)
		os.Exit(0)
	}
	
	// Get credentials
	credentials, err = getCredentials(*environment)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to load credentials: %v", err))
		log.Fatalf("Failed to load credentials: %v", err)
	}
	
	// Mask token in logs
	maskedToken := credentials.Token
	if len(maskedToken) > 8 {
		maskedToken = maskedToken[:4] + "..." + maskedToken[len(maskedToken)-4:]
	}
	logger.Log("INFO", fmt.Sprintf("Credentials loaded (token: %s)", maskedToken))
	
	// Setup routes
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/api/update", updateHandler)
	http.HandleFunc("/health", healthHandler)
	
	// Start server
	addr := fmt.Sprintf(":%d", *port)
	url := fmt.Sprintf("http://localhost%s", addr)
	logger.Log("INFO", fmt.Sprintf("Server starting on %s", url))
	
	if *environment == "production" {
		fmt.Println("\n⚠️  WARNING: Running in PRODUCTION mode!")
		fmt.Printf("Managing domain: %s\n", credentials.Domain)
		fmt.Println("Press Ctrl+C to stop\n")
	}
	
	// Start server in a goroutine so we can open the browser
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Server failed: %v", err))
			log.Fatalf("Server failed: %v", err)
		}
	}()
	
	// Give the server a moment to start
	time.Sleep(500 * time.Millisecond)
	
	// Open browser unless disabled
	if !*noBrowser {
		openBrowser(url)
	}
	
	fmt.Printf("\n🌐 Web interface available at: %s\n", url)
	fmt.Println("Press Ctrl+C to stop")
	
	// Keep the main goroutine alive
	select {}
}
