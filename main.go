package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	// Unique identifier (persistent)
	UniqueID        string `json:"unique_id"`                   // Unique hash-based identifier
	
	// Custom fields (persistent)
	Alias           string `json:"alias"`
	Description     string `json:"description"`
	Region          string `json:"region,omitempty"`
	Account         string `json:"account,omitempty"`           // Pool1, Pool2, Pool3, Zgirt, Jetski
	Container       string `json:"container,omitempty"`         // Group1, Group2, Test
	Notes           string `json:"notes,omitempty"`             // User editable notes
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
	Environment       string    `json:"environment"`
	Domain            string    `json:"domain"`
	LastSync          time.Time `json:"last_sync"`
	Servers           []Server  `json:"servers"`
	AvailableAccounts []string  `json:"available_accounts,omitempty"`   // Available account tags
	AvailableContainers []string `json:"available_containers,omitempty"` // Available container tags
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

// generateServerID creates a unique identifier for a server based on its DNS name and IP
func generateServerID(name, ip string) string {
	data := fmt.Sprintf("%s:%s", name, ip)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8]) // Use first 8 bytes for a shorter ID
}

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
        .server-grid {
            display: flex;
            flex-direction: column;
            gap: 15px;
            margin-bottom: 20px;
            max-width: 1000px;
            margin-left: auto;
            margin-right: auto;
        }
        .server-card {
            background-color: white;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            padding: 20px;
            transition: all 0.3s ease;
            border: 2px solid transparent;
        }
        .server-card:hover {
            box-shadow: 0 4px 8px rgba(0,0,0,0.15);
        }
        .server-card.active {
            border: 2px solid #28a745;
            background-color: #f0fff4;
            box-shadow: 0 4px 12px rgba(40, 167, 69, 0.2);
        }
        .server-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 15px;
            padding-bottom: 12px;
            border-bottom: 3px solid #f0f0f0;
        }
        .server-card.active .server-header {
            border-bottom-color: #28a745;
        }
        .server-name {
            font-size: 20px;
            font-weight: bold;
            color: #333;
        }
        .server-card.active .server-name {
            color: #28a745;
        }
        .server-ip {
            font-size: 16px;
            color: #666;
            font-weight: 500;
            margin-top: 4px;
            background-color: #f0f0f0;
            padding: 4px 10px;
            border-radius: 4px;
            font-family: monospace;
        }
        .server-card.active .server-ip {
            background-color: #d4edda;
            color: #155724;
        }
        .server-status {
            display: flex;
            align-items: center;
            gap: 10px;
        }
        .entries-container {
            margin-top: 15px;
        }
        .entry-row {
            background-color: #f9f9f9;
            border: 2px solid #e8e8e8;
            border-radius: 6px;
            margin-bottom: 10px;
            padding: 15px;
            transition: all 0.2s ease;
            position: relative;
        }
        .entry-row:hover {
            background-color: #f5f5f5;
            border-color: #ddd;
        }
        .entry-row.active {
            border-color: #28a745;
            background-color: #e7f5e7;
            box-shadow: 0 2px 8px rgba(40, 167, 69, 0.15);
        }
        .entry-main {
            display: flex;
            align-items: center;
            justify-content: space-between;
            margin-bottom: 8px;
        }
        .entry-info {
            display: flex;
            align-items: center;
            gap: 15px;
            flex: 1;
        }
        .entry-name {
            font-weight: 600;
            color: #333;
            min-width: 100px;
        }
        .entry-details {
            display: flex;
            align-items: center;
            gap: 12px;
            font-size: 13px;
            color: #666;
        }
        .entry-tags {
            display: flex;
            flex-direction: column;
            gap: 8px;
            padding-top: 8px;
            border-top: 1px solid #eee;
        }
        .entry-checkbox {
            width: 18px;
            height: 18px;
            cursor: pointer;
        }
        .proxy-badge {
            padding: 2px 8px;
            border-radius: 3px;
            font-size: 12px;
            font-weight: 500;
        }
        .proxy-on {
            background-color: #fff3e0;
            color: #e65100;
        }
        .proxy-off {
            background-color: #f5f5f5;
            color: #666;
        }
        .status-indicator {
            width: 10px;
            height: 10px;
            border-radius: 50%;
            display: inline-block;
        }
        .status-active {
            background-color: #4caf50;
        }
        .status-inactive {
            background-color: #ccc;
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
        .server-meta {
            margin-top: 10px;
            padding-top: 10px;
            border-top: 1px solid #f0f0f0;
            font-size: 12px;
            color: #666;
        }
        .server-notes {
            margin-top: 15px;
            padding-top: 15px;
            border-top: 1px solid #f0f0f0;
        }
        .notes-label {
            font-size: 12px;
            font-weight: 600;
            color: #666;
            margin-bottom: 5px;
        }
        .notes-textarea {
            width: 100%;
            min-height: 60px;
            padding: 8px;
            border: 1px solid #ddd;
            border-radius: 4px;
            font-size: 13px;
            font-family: inherit;
            resize: vertical;
            transition: border-color 0.2s ease;
        }
        .notes-textarea:focus {
            outline: none;
            border-color: #999;
        }
        .tag-label {
            font-size: 11px;
            font-weight: 600;
            color: #666;
            margin-right: 8px;
            min-width: 70px;
            display: inline-block;
        }
        .tag-group {
            display: flex;
            align-items: center;
            gap: 8px;
        }
        .add-tag-btn {
            width: 22px;
            height: 22px;
            padding: 0;
            border: 1px solid #ddd;
            background-color: #f8f8f8;
            border-radius: 3px;
            cursor: pointer;
            font-size: 14px;
            line-height: 1;
            color: #666;
            transition: all 0.2s ease;
        }
        .add-tag-btn:hover {
            background-color: #e8e8e8;
            border-color: #999;
            color: #333;
        }
        .tag-select {
            font-size: 12px;
            padding: 2px 4px;
            border: 1px solid #ddd;
            border-radius: 3px;
            background-color: white;
            min-width: 80px;
        }
        .tag-badge {
            display: inline-block;
            padding: 2px 6px;
            border-radius: 3px;
            font-size: 11px;
            font-weight: 500;
            margin-right: 4px;
        }
        .tag-account {
            background-color: #e3f2fd;
            color: #1976d2;
        }
        .tag-container {
            background-color: #f3e5f5;
            color: #7b1fa2;
        }
        .dns-form-container {
            background-color: white;
            padding: 20px;
            border-radius: 8px;
            margin-bottom: 20px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        .dns-form-title {
            font-size: 18px;
            font-weight: bold;
            margin-bottom: 15px;
            color: #333;
        }
        .dns-form {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 15px;
            margin-bottom: 15px;
        }
        .form-group {
            display: flex;
            flex-direction: column;
        }
        .form-label {
            font-size: 14px;
            color: #666;
            margin-bottom: 5px;
            font-weight: 500;
        }
        .form-input {
            padding: 8px 12px;
            border: 1px solid #ddd;
            border-radius: 4px;
            font-size: 14px;
            transition: border-color 0.2s ease;
        }
        .form-input:focus {
            outline: none;
            border-color: {{if eq .Environment "production"}}#dc3545{{else}}#fd7e14{{end}};
        }
        .form-checkbox {
            display: flex;
            align-items: center;
            margin-top: 20px;
        }
        .form-checkbox input {
            margin-right: 8px;
            width: 18px;
            height: 18px;
            cursor: pointer;
        }
        .form-actions {
            display: flex;
            gap: 10px;
            margin-top: 15px;
        }
        .btn-add-dns {
            background-color: {{if eq .Environment "production"}}#dc3545{{else}}#fd7e14{{end}};
            color: white;
            padding: 10px 20px;
            border: none;
            border-radius: 5px;
            cursor: pointer;
            font-size: 14px;
            font-weight: 500;
            transition: background-color 0.2s ease;
        }
        .btn-add-dns:hover {
            background-color: {{if eq .Environment "production"}}#c82333{{else}}#e96810{{end}};
        }
        .btn-add-dns:disabled {
            background-color: #ccc;
            cursor: not-allowed;
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>XMR Server Manager - {{.Environment | toUpper}} Environment</h1>
        <p>Domain: {{.Domain}}</p>
    </div>
    
    <div class="info">
        <strong>Total Servers:</strong> {{.TotalServers}} | 
        <strong>Active Entries:</strong> {{.ActiveCount}} | 
        <strong>Inactive Entries:</strong> {{.InactiveCount}} |
        <strong>Last Updated:</strong> <span id="lastUpdate">{{.LastUpdate}}</span>
    </div>
    
    <form id="serverForm" onsubmit="updateServers(event)">
        <div class="server-grid">
            {{range .ServerGroups}}
            <div class="server-card {{if .HasActiveEntries}}active{{end}}">
                <div class="server-header">
                    <div>
                        <div class="server-name">{{.Names}}</div>
                        <div class="server-ip">{{.IP}}</div>
                    </div>
                    <div class="server-status">
                        <span class="status-indicator {{if .HasActiveEntries}}status-active{{else}}status-inactive{{end}}"></span>
                        <span>{{if .HasActiveEntries}}Active{{else}}Inactive{{end}}</span>
                    </div>
                </div>
                
                <div class="entries-container">
                    {{range .Entries}}
                    <div class="entry-row">
                        <div class="entry-main">
                            <div class="entry-info">
                                <span class="entry-name">{{.Name}}</span>
                                <div class="entry-details">
                                    {{if .Proxied}}
                                        <span class="proxy-badge proxy-on">Proxied</span>
                                    {{else}}
                                        <span class="proxy-badge proxy-off">DNS only</span>
                                    {{end}}
                                    <span>TTL: {{.TTL}}s</span>
                                </div>
                            </div>
                            <input type="checkbox" class="entry-checkbox" 
                                   name="active" 
                                   value="{{$.IP}}-{{.Name}}" 
                                   data-ip="{{$.IP}}"
                                   data-name="{{.Name}}"
                                   data-alias="{{.Alias}}"
                                   data-proxied="{{.Proxied}}"
                                   data-ttl="{{.TTL}}"
                                   data-account="{{.Account}}"
                                   data-container="{{.Container}}"
                                   {{if .IsActive}}checked{{end}}>
                        </div>
                        <div class="entry-tags">
                            <div class="tag-group">
                                <span class="tag-label">Account:</span>
                                <select class="tag-select account-select" 
                                        data-unique-id="{{.UniqueID}}"
                                        data-ip="{{$.IP}}" 
                                        data-name="{{.Name}}"
                                        data-current-value="{{.Account}}"
                                        onchange="updateTag(this, 'account')">
                                    <option value="">None</option>
                                </select>
                                <button type="button" class="add-tag-btn" onclick="addTag('account')" title="Add new account">+</button>
                            </div>
                            <div class="tag-group">
                                <span class="tag-label">Container:</span>
                                <select class="tag-select container-select" 
                                        data-unique-id="{{.UniqueID}}"
                                        data-ip="{{$.IP}}" 
                                        data-name="{{.Name}}"
                                        data-current-value="{{.Container}}"
                                        onchange="updateTag(this, 'container')">
                                    <option value="">None</option>
                                </select>
                                <button type="button" class="add-tag-btn" onclick="addTag('container')" title="Add new container">+</button>
                            </div>
                        </div>
                    </div>
                    {{end}}
                </div>
                
                <div class="server-notes">
                    <div class="notes-label">Notes</div>
                    <textarea class="notes-textarea" 
                              placeholder="Add notes about this server..."
                              data-ip="{{.IP}}"
                              onblur="updateNotes(this)">{{.Notes}}</textarea>
                </div>
            </div>
            {{end}}
        </div>
        
        <button type="submit" class="button" {{if eq .Environment "production"}}onclick="return confirmProduction()"{{end}}>
            Update DNS Records
        </button>
    </form>
    
    <!-- Add DNS Entry Form -->
    <div class="dns-form-container">
        <div class="dns-form-title">Add New DNS Entry</div>
        <form id="addDnsForm" class="dns-form" onsubmit="addDNSEntry(event)">
            <div class="form-group">
                <label for="newDnsName">DNS Name:</label>
                <input type="text" id="newDnsName" name="name" required 
                       placeholder="e.g., server1 or us.server1"
                       pattern="[a-zA-Z0-9\-\.]+"
                       title="Only letters, numbers, dots and hyphens allowed">
            </div>
            <div class="form-group">
                <label for="newDnsIP">IP Address:</label>
                <input type="text" id="newDnsIP" name="ip" required 
                       placeholder="e.g., 192.168.1.1"
                       pattern="^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$"
                       title="Please enter a valid IP address">
            </div>
            <div class="form-group">
                <label for="newDnsAlias">Alias (Optional):</label>
                <input type="text" id="newDnsAlias" name="alias" 
                       placeholder="Friendly name">
            </div>
            <div class="form-group">
                <label for="newDnsTTL">TTL (seconds):</label>
                <input type="number" id="newDnsTTL" name="ttl" 
                       value="60" min="60" max="86400">
            </div>
            <div class="form-group">
                <label for="newDnsProxied">
                    <input type="checkbox" id="newDnsProxied" name="proxied">
                    Proxied through Cloudflare
                </label>
            </div>
            <div class="form-group" style="grid-column: 1 / -1;">
                <button type="submit" class="btn-add-dns">Add DNS Entry</button>
            </div>
        </form>
    </div>
    
    <div id="status" class="status"></div>
    
    <div id="operationLog" style="margin-top: 20px; display: none;">
        <h3>Operation Log:</h3>
        <div id="logEntries" style="background-color: white; padding: 10px; border-radius: 5px; max-height: 300px; overflow-y: auto;"></div>
    </div>
    
    <script>
        // Populate tag dropdowns on page load
        window.onload = function() {
            const availableAccounts = {{.AvailableAccounts}};
            const availableContainers = {{.AvailableContainers}};
            
            // Add event listeners to checkboxes to update visual state
            const checkboxes = document.querySelectorAll('.entry-checkbox');
            checkboxes.forEach(checkbox => {
                // Set initial state
                updateEntryVisualState(checkbox);
                
                // Add change listener
                checkbox.addEventListener('change', function() {
                    updateEntryVisualState(this);
                    updateServerCardState(this);
                });
            });
            
            // Update server card states on load
            updateAllServerCardStates();
            
            // Populate all account dropdowns
            const accountSelects = document.querySelectorAll('.account-select');
            accountSelects.forEach(select => {
                const currentValue = select.dataset.currentValue;
                availableAccounts.forEach(account => {
                    const option = document.createElement('option');
                    option.value = account;
                    option.textContent = account;
                    if (account === currentValue) {
                        option.selected = true;
                    }
                    select.appendChild(option);
                });
                // Store current value for revert
                select.dataset.previousValue = currentValue || '';
            });
            
            // Populate all container dropdowns
            const containerSelects = document.querySelectorAll('.container-select');
            containerSelects.forEach(select => {
                const currentValue = select.dataset.currentValue;
                availableContainers.forEach(container => {
                    const option = document.createElement('option');
                    option.value = container;
                    option.textContent = container;
                    if (container === currentValue) {
                        option.selected = true;
                    }
                    select.appendChild(option);
                });
                // Store current value for revert
                select.dataset.previousValue = currentValue || '';
            });
        };
        
        // Function to update entry row visual state
        function updateEntryVisualState(checkbox) {
            const entryRow = checkbox.closest('.entry-row');
            if (entryRow) {
                if (checkbox.checked) {
                    entryRow.classList.add('active');
                } else {
                    entryRow.classList.remove('active');
                }
            }
        }
        
        // Function to update server card state based on its entries
        function updateServerCardState(checkbox) {
            const serverCard = checkbox.closest('.server-card');
            if (serverCard) {
                const allCheckboxes = serverCard.querySelectorAll('.entry-checkbox');
                const hasActive = Array.from(allCheckboxes).some(cb => cb.checked);
                
                if (hasActive) {
                    serverCard.classList.add('active');
                } else {
                    serverCard.classList.remove('active');
                }
            }
        }
        
        // Function to update all server card states
        function updateAllServerCardStates() {
            const serverCards = document.querySelectorAll('.server-card');
            serverCards.forEach(card => {
                const allCheckboxes = card.querySelectorAll('.entry-checkbox');
                const hasActive = Array.from(allCheckboxes).some(cb => cb.checked);
                
                if (hasActive) {
                    card.classList.add('active');
                } else {
                    card.classList.remove('active');
                }
            });
        }
        
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
            
            // Build request data with all fields including tags
            const servers = [];
            document.querySelectorAll('input[name="active"]:checked').forEach(checkbox => {
                // Find the corresponding select elements in the same entry
                const entryRow = checkbox.closest('.entry-row');
                const accountSelect = entryRow.querySelector('.account-select');
                const containerSelect = entryRow.querySelector('.container-select');
                
                servers.push({
                    ip: checkbox.dataset.ip,
                    name: checkbox.dataset.name,
                    alias: checkbox.dataset.alias,
                    account: accountSelect ? accountSelect.value : '',
                    container: containerSelect ? containerSelect.value : '',
                    proxied: checkbox.dataset.proxied === 'true',
                    ttl: parseInt(checkbox.dataset.ttl) || 60,
                    active: true
                });
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
        
        // Function to update tags
        function updateTag(selectElement, tagType) {
            const uniqueId = selectElement.dataset.uniqueId;
            const ip = selectElement.dataset.ip;
            const name = selectElement.dataset.name;
            const value = selectElement.value;
            
            // Send update to server
            fetch('/api/update-tag', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    unique_id: uniqueId,
                    ip: ip,
                    name: name,
                    tag_type: tagType,
                    value: value
                })
            })
            .then(response => response.json())
            .then(data => {
                if (!data.success) {
                    alert('Failed to update tag: ' + data.message);
                    // Revert the selection on failure
                    selectElement.value = selectElement.dataset.previousValue || '';
                }
            })
            .catch(error => {
                alert('Error updating tag: ' + error.message);
                selectElement.value = selectElement.dataset.previousValue || '';
            });
            
            // Store current value for potential revert
            selectElement.dataset.previousValue = value;
        }
        
        // Function to update notes
        function updateNotes(textarea) {
            const ip = textarea.dataset.ip;
            const notes = textarea.value;
            
            // Send update to server
            fetch('/api/update-notes', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    ip: ip,
                    notes: notes
                })
            })
            .then(response => response.json())
            .then(data => {
                if (!data.success) {
                    alert('Failed to update notes: ' + data.message);
                    // Revert the text on failure
                    textarea.value = textarea.dataset.previousValue || '';
                }
            })
            .catch(error => {
                alert('Error updating notes: ' + error.message);
                textarea.value = textarea.dataset.previousValue || '';
            });
            
            // Store current value for potential revert
            textarea.dataset.previousValue = notes;
        }
        
        // Function to add a new tag
        function addTag(tagType) {
            const newTag = prompt('Enter new ' + tagType + ':');
            if (!newTag || newTag.trim() === '') {
                return;
            }
            
            // Send request to add new tag
            fetch('/api/add-tag', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    tag_type: tagType,
                    tag_name: newTag.trim()
                })
            })
            .then(response => response.json())
            .then(data => {
                if (data.success) {
                    // Reload the page to show the new tag in all dropdowns
                    location.reload();
                } else {
                    alert('Failed to add tag: ' + data.message);
                }
            })
            .catch(error => {
                alert('Error adding tag: ' + error.message);
            });
        }
        
        // Function to add DNS entry
        async function addDNSEntry(event) {
            event.preventDefault();
            
            const form = event.target;
            const formData = new FormData(form);
            
            const dnsEntry = {
                name: formData.get('name'),
                ip: formData.get('ip'),
                alias: formData.get('alias') || formData.get('name'),
                ttl: parseInt(formData.get('ttl')) || 60,
                proxied: formData.get('proxied') === 'on'
            };
            
            // Show loading state
            const submitBtn = form.querySelector('.btn-add-dns');
            const originalText = submitBtn.textContent;
            submitBtn.disabled = true;
            submitBtn.textContent = 'Adding...';
            
            // Show status
            const statusDiv = document.getElementById('status');
            
            try {
                const response = await fetch('/api/dns/create', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                    },
                    body: JSON.stringify(dnsEntry)
                });
                
                const result = await response.json();
                
                if (response.ok) {
                    statusDiv.textContent = 'DNS entry added successfully! Refreshing...';
                    statusDiv.style.color = 'green';
                    statusDiv.style.display = 'block';
                    form.reset();
                    // Reload page to show new entry
                    setTimeout(() => {
                        window.location.reload();
                    }, 1500);
                } else {
                    statusDiv.textContent = 'Error: ' + (result.error || 'Failed to add DNS entry');
                    statusDiv.style.color = 'red';
                    statusDiv.style.display = 'block';
                }
            } catch (error) {
                statusDiv.textContent = 'Error: ' + error.message;
                statusDiv.style.color = 'red';
                statusDiv.style.display = 'block';
            } finally {
                submitBtn.disabled = false;
                submitBtn.textContent = originalText;
            }
        }
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
	// Get all A records that end with the domain (including subdomains like us.xmr)
	req, err := c.makeRequest("GET", fmt.Sprintf("/dns_records?type=A&name~end=%s", c.credentials.Domain), nil)
	if err != nil {
		return nil, err
	}
	
	logger.Log("INFO", fmt.Sprintf("Fetching DNS records ending with %s", c.credentials.Domain))
	
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
	
	// Filter to only include records ending with our domain
	var filteredRecords []CloudflareRecord
	for _, record := range cfResp.Result {
		if strings.HasSuffix(record.Name, c.credentials.Domain) {
			filteredRecords = append(filteredRecords, record)
		}
	}
	
	logger.Log("INFO", fmt.Sprintf("Found %d DNS records for domain %s", len(filteredRecords), c.credentials.Domain))
	return filteredRecords, nil
}

func (c *CloudflareClient) CreateDNSRecord(ip, dnsName, alias string, proxied bool, ttl int) (string, error) {
	if ttl <= 0 {
		ttl = 60 // Default to 1 minute
	}
	
	// Construct full DNS name
	fullName := dnsName
	if !strings.Contains(dnsName, ".") {
		fullName = dnsName + "." + c.credentials.Domain
	} else if !strings.HasSuffix(dnsName, c.credentials.Domain) {
		fullName = dnsName + "." + c.credentials.Domain
	}
	
	payload := map[string]interface{}{
		"type":    "A",
		"name":    fullName,
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
	
	// Initialize default available tags if not present
	if len(config.AvailableAccounts) == 0 {
		config.AvailableAccounts = []string{"Pool1", "Pool2", "Pool3", "Zgirt", "Jetski"}
	}
	if len(config.AvailableContainers) == 0 {
		config.AvailableContainers = []string{"Group1", "Group2", "Test"}
	}
	
	// Migrate: Add unique IDs to servers that don't have them
	modified := false
	for i := range config.Servers {
		if config.Servers[i].UniqueID == "" {
			config.Servers[i].UniqueID = generateServerID(config.Servers[i].Name, config.Servers[i].Content)
			modified = true
			logger.Log("INFO", fmt.Sprintf("Generated UniqueID for server %s (%s): %s", 
				config.Servers[i].Name, config.Servers[i].Content, config.Servers[i].UniqueID))
		}
	}
	
	// Save if we modified the config
	if modified {
		if err := saveServerConfig(env, &config); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to save migrated config: %v", err))
		} else {
			logger.Log("INFO", "Saved config with generated unique IDs")
		}
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
			// Unique identifier
			UniqueID:        generateServerID(record.Name, record.Content),
			
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
				AvailableAccounts: []string{"Pool1", "Pool2", "Pool3", "Zgirt", "Jetski"},
				AvailableContainers: []string{"Group1", "Group2", "Test"},
			}
		}
	}
	
	// Build active IP map
	activeIPs := make(map[string]CloudflareRecord)
	for _, record := range records {
		activeIPs[record.Content] = record
	}
	
	// Prepare template data
	type DNSEntry struct {
		UniqueID  string // Unique identifier for this entry
		Name      string // The DNS name (e.g., "xmr", "us.xmr")
		Alias     string // The descriptive alias from config
		Account   string // Account tag
		Container string // Container tag
		Proxied   bool
		TTL       int
		IsActive  bool
		RecordID  string
	}
	
	type ServerGroup struct {
		IP               string
		Names            string // Concatenated aliases separated by semicolon
		Notes            string // Editable notes for this server
		Entries          []DNSEntry
		HasActiveEntries bool
	}
	
	// First, create a map of all known servers from config indexed by UniqueID
	configByID := make(map[string]Server)
	for _, server := range config.Servers {
		if server.UniqueID != "" {
			configByID[server.UniqueID] = server
		}
	}
	
	// Also create a fallback map indexed by IP+Name for servers without UniqueID
	type serverKey struct {
		ip   string
		name string
	}
	configByKey := make(map[serverKey]Server)
	for _, server := range config.Servers {
		// Extract DNS name for the key
		dnsName := strings.TrimSuffix(server.Name, "."+credentials.Domain)
		key := serverKey{ip: server.Content, name: dnsName}
		configByKey[key] = server
	}
	
	// Group all DNS records by IP address
	serverGroupsMap := make(map[string]*ServerGroup)
	activeCount := 0
	
	// Process all active DNS records from Cloudflare
	for _, record := range records {
		ip := record.Content
		
		// Get or create server group for this IP
		if _, exists := serverGroupsMap[ip]; !exists {
			serverGroupsMap[ip] = &ServerGroup{
				IP:      ip,
				Names:   "",
				Notes:   "",
				Entries: []DNSEntry{},
			}
		}
		
		// Extract the subdomain name (e.g., "xmr" from "xmr.domain.com" or "us.xmr" from "us.xmr.domain.com")
		dnsName := strings.TrimSuffix(record.Name, "."+credentials.Domain)
		
		// Generate unique ID for this entry
		uniqueID := generateServerID(record.Name, ip)
		
		// Get alias, account, and container from config if available
		alias := record.Comment
		account := ""
		container := ""
		
		// First try to find by UniqueID
		if configServer, exists := configByID[uniqueID]; exists {
			if configServer.Alias != "" {
				alias = configServer.Alias
			}
			account = configServer.Account
			container = configServer.Container
		} else {
			// Fallback to key-based lookup
			key := serverKey{ip: ip, name: dnsName}
			if configServer, exists := configByKey[key]; exists {
				if configServer.Alias != "" {
					alias = configServer.Alias
				}
				account = configServer.Account
				container = configServer.Container
			}
		}
		
		if alias == "" {
			alias = dnsName
		}
		
		entry := DNSEntry{
			UniqueID:  uniqueID,
			Name:      dnsName,
			Alias:     alias,
			Account:   account,
			Container: container,
			Proxied:   record.Proxied,
			TTL:       record.TTL,
			IsActive:  true,
			RecordID:  record.ID,
		}
		
		serverGroupsMap[ip].Entries = append(serverGroupsMap[ip].Entries, entry)
		serverGroupsMap[ip].HasActiveEntries = true
		activeCount++
		
		// Add alias to the group's name list
		if serverGroupsMap[ip].Names == "" {
			serverGroupsMap[ip].Names = alias
		} else if !strings.Contains(serverGroupsMap[ip].Names, alias) {
			serverGroupsMap[ip].Names += "; " + alias
		}
		
		// Get notes from config if available (use the first non-empty notes found)
		if serverGroupsMap[ip].Notes == "" {
			if cfgServer, exists := configByID[uniqueID]; exists && cfgServer.Notes != "" {
				serverGroupsMap[ip].Notes = cfgServer.Notes
			} else {
				key := serverKey{ip: ip, name: dnsName}
				if cfgServer, exists := configByKey[key]; exists && cfgServer.Notes != "" {
					serverGroupsMap[ip].Notes = cfgServer.Notes
				}
			}
		}
	}
	
	// Add inactive servers from config
	for _, server := range config.Servers {
		if _, isActive := activeIPs[server.Content]; !isActive {
			ip := server.Content
			
			// Get or create server group
			if _, exists := serverGroupsMap[ip]; !exists {
				serverGroupsMap[ip] = &ServerGroup{
					IP:      ip,
					Names:   server.Alias,
					Notes:   server.Notes,
					Entries: []DNSEntry{},
				}
			} else if serverGroupsMap[ip].Notes == "" && server.Notes != "" {
				// Add notes if not already present
				serverGroupsMap[ip].Notes = server.Notes
			}
			
			// Extract DNS name
			dnsName := strings.TrimSuffix(server.Name, "."+credentials.Domain)
			if dnsName == server.Name {
				dnsName = "xmr" // Default if no domain match
			}
			
			// Generate unique ID if not present
			uniqueID := server.UniqueID
			if uniqueID == "" {
				uniqueID = generateServerID(server.Name, server.Content)
			}
			
			entry := DNSEntry{
				UniqueID:  uniqueID,
				Name:      dnsName,
				Alias:     server.Alias,
				Account:   server.Account,
				Container: server.Container,
				Proxied:   server.Proxied,
				TTL:       server.TTL,
				IsActive:  false,
				RecordID:  "",
			}
			
			serverGroupsMap[ip].Entries = append(serverGroupsMap[ip].Entries, entry)
		}
	}
	
	// Convert map to sorted slice
	var serverGroups []ServerGroup
	for _, group := range serverGroupsMap {
		serverGroups = append(serverGroups, *group)
	}
	
	// Sort server groups by IP address
	sort.Slice(serverGroups, func(i, j int) bool {
		return serverGroups[i].IP < serverGroups[j].IP
	})
	
	// Count total unique servers (by IP)
	totalServers := len(serverGroupsMap)
	
	data := map[string]interface{}{
		"Environment":        *environment,
		"Domain":             config.Domain,
		"ServerGroups":       serverGroups,
		"TotalServers":       totalServers,
		"ActiveCount":        activeCount,
		"InactiveCount":      len(config.Servers) - activeCount,
		"LastUpdate":         time.Now().Format("2006-01-02 15:04:05"),
		"AvailableAccounts":  config.AvailableAccounts,
		"AvailableContainers": config.AvailableContainers,
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
		IP        string `json:"ip"`
		Name      string `json:"name"`      // DNS name like "xmr" or "us.xmr"
		Alias     string `json:"alias"`
		Account   string `json:"account"`   // Account tag
		Container string `json:"container"` // Container tag
		Proxied   bool   `json:"proxied"`
		TTL       int    `json:"ttl"`
		Active    bool   `json:"active"`
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
	
	// Build map of current records by key (name+ip)
	type recordKey struct {
		name string
		ip   string
	}
	currentRecords := make(map[recordKey]CloudflareRecord)
	for _, record := range records {
		// Extract subdomain
		dnsName := strings.TrimSuffix(record.Name, "."+credentials.Domain)
		key := recordKey{name: dnsName, ip: record.Content}
		currentRecords[key] = record
	}
	
	// Build map of requested records
	requestedRecords := make(map[recordKey]struct {
		alias     string
		account   string
		container string
		proxied   bool
		ttl       int
	})
	for _, server := range req.ActiveServers {
		key := recordKey{name: server.Name, ip: server.IP}
		requestedRecords[key] = struct {
			alias     string
			account   string
			container string
			proxied   bool
			ttl       int
		}{
			alias:     server.Alias,
			account:   server.Account,
			container: server.Container,
			proxied:   server.Proxied,
			ttl:       server.TTL,
		}
	}
	
	// Load server config for updating timestamps
	config, err := loadServerConfig(*environment)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to load config for timestamp updates: %v", err))
		// Create empty config if it doesn't exist
		config = &ServerConfig{
			Environment: *environment,
			Domain:      credentials.Domain,
			Servers:     []Server{},
		}
	}
	
	// Process changes
	changes := 0
	
	// Add new records
	for key, info := range requestedRecords {
		if _, exists := currentRecords[key]; !exists {
			detail := struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			}{}
			
			_, err := cfClient.CreateDNSRecord(key.ip, key.name, info.alias, info.proxied, info.ttl)
			if err != nil {
				detail.Message = fmt.Sprintf("Failed to activate %s (%s -> %s): %v", key.name, info.alias, key.ip, err)
				detail.Status = "error"
			} else {
				proxyStatus := "DNS-only"
				if info.proxied {
					proxyStatus = "proxied"
				}
				detail.Message = fmt.Sprintf("✓ Activated %s (%s -> %s) [%s, TTL: %d]", key.name, info.alias, key.ip, proxyStatus, info.ttl)
				detail.Status = "success"
				changes++
				
				// Update or add to config
				found := false
				for i := range config.Servers {
					if config.Servers[i].Content == key.ip && config.Servers[i].Name == key.name+"."+credentials.Domain {
						config.Servers[i].LastActivatedOn = time.Now().Format(time.RFC3339)
						found = true
						break
					}
				}
				if !found {
					// Add new server to config
					now := time.Now().Format(time.RFC3339)
					fullName := key.name + "." + credentials.Domain
					config.Servers = append(config.Servers, Server{
						UniqueID:        generateServerID(fullName, key.ip),
						Alias:           info.alias,
						Account:         info.account,
						Container:       info.container,
						Description:     fmt.Sprintf("Added via web UI on %s", time.Now().Format("2006-01-02")),
						FirstSeenOn:     now,
						LastActivatedOn: now,
						Type:            "A",
						Name:            fullName,
						Content:         key.ip,
						TTL:             info.ttl,
						Proxied:         info.proxied,
						Comment:         info.alias,
					})
				}
			}
			
			response.Details = append(response.Details, detail)
		}
	}
	
	// Remove records
	for key, record := range currentRecords {
		if _, shouldExist := requestedRecords[key]; !shouldExist {
			detail := struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			}{}
			
			err := cfClient.DeleteDNSRecord(record.ID)
			if err != nil {
				detail.Message = fmt.Sprintf("Failed to deactivate %s (%s -> %s): %v", key.name, record.Comment, key.ip, err)
				detail.Status = "error"
			} else {
				detail.Message = fmt.Sprintf("✓ Deactivated %s (%s -> %s)", key.name, record.Comment, key.ip)
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

// createDNSHandler handles creating new DNS entries
func createDNSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	// Parse request body
	var req struct {
		Name    string `json:"name"`
		IP      string `json:"ip"`
		Alias   string `json:"alias"`
		TTL     int    `json:"ttl"`
		Proxied bool   `json:"proxied"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Invalid request body",
		})
		return
	}
	
	// Validate inputs
	if req.Name == "" || req.IP == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Name and IP are required",
		})
		return
	}
	
	// Set default TTL if not provided
	if req.TTL <= 0 {
		req.TTL = 60
	}
	
	// Set alias to name if not provided
	if req.Alias == "" {
		req.Alias = req.Name
	}
	
	// Create Cloudflare client
	cfClient := NewCloudflareClient(credentials)
	
	// Create DNS record
	recordID, err := cfClient.CreateDNSRecord(req.IP, req.Name, req.Alias, req.Proxied, req.TTL)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to create DNS record: %v", err))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("Failed to create DNS record: %v", err),
		})
		return
	}
	
	// Update server config
	config, err := loadServerConfig(*environment)
	if err != nil {
		// Create new config if it doesn't exist
		config = &ServerConfig{
			Environment: *environment,
			Domain:      credentials.Domain,
			Servers:     []Server{},
		}
	}
	
	// Add new server to config
	fullName := req.Name
	if !strings.Contains(req.Name, ".") {
		fullName = req.Name + "." + credentials.Domain
	} else if !strings.HasSuffix(req.Name, credentials.Domain) {
		fullName = req.Name + "." + credentials.Domain
	}
	
	now := time.Now().Format(time.RFC3339)
	newServer := Server{
		UniqueID:        generateServerID(fullName, req.IP),
		Alias:           req.Alias,
		Type:            "A",
		Name:            fullName,
		Content:         req.IP,
		TTL:             req.TTL,
		Proxied:         req.Proxied,
		FirstSeenOn:     now,
		LastActivatedOn: now,
	}
	
	// Check if server already exists in config
	found := false
	for i, server := range config.Servers {
		if server.UniqueID == newServer.UniqueID {
			config.Servers[i].LastActivatedOn = now
			found = true
			break
		}
	}
	
	if !found {
		config.Servers = append(config.Servers, newServer)
	}
	
	// Save updated config
	if err := saveServerConfig(*environment, config); err != nil {
		logger.Log("WARNING", fmt.Sprintf("DNS record created but failed to save config: %v", err))
	}
	
	logger.Log("INFO", fmt.Sprintf("Created DNS record: %s -> %s (ID: %s)", req.Name, req.IP, recordID))
	
	// Return success response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("DNS record created successfully: %s -> %s", req.Name, req.IP),
		"id":      recordID,
	})
}

// Tag update handler
func updateTagHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req struct {
		UniqueID string `json:"unique_id"`
		IP       string `json:"ip"`
		Name     string `json:"name"`
		TagType  string `json:"tag_type"`
		Value    string `json:"value"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	
	// Load configuration
	config, err := loadServerConfig(*environment)
	if err != nil || config == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Failed to load configuration",
		})
		return
	}
	
	// Find and update the server
	found := false
	
	// First try to find by UniqueID
	if req.UniqueID != "" {
		for i := range config.Servers {
			if config.Servers[i].UniqueID == req.UniqueID {
				if req.TagType == "account" {
					config.Servers[i].Account = req.Value
				} else if req.TagType == "container" {
					config.Servers[i].Container = req.Value
				}
				found = true
				logger.Log("INFO", fmt.Sprintf("Found and updated server by UniqueID: %s (%s)", config.Servers[i].Name, req.IP))
				break
			}
		}
	}
	
	// If not found by UniqueID, try fallback method
	if !found {
		for i := range config.Servers {
			// Extract DNS name from full name for comparison
			serverDNSName := strings.TrimSuffix(config.Servers[i].Name, "."+credentials.Domain)
			
			if config.Servers[i].Content == req.IP && serverDNSName == req.Name {
				// Generate and save UniqueID if missing
				if config.Servers[i].UniqueID == "" {
					config.Servers[i].UniqueID = generateServerID(config.Servers[i].Name, config.Servers[i].Content)
				}
				
				if req.TagType == "account" {
					config.Servers[i].Account = req.Value
				} else if req.TagType == "container" {
					config.Servers[i].Container = req.Value
				}
				found = true
				logger.Log("INFO", fmt.Sprintf("Found and updated server by IP+Name: %s (%s)", config.Servers[i].Name, req.IP))
				break
			}
		}
	}
	
	// If not found in config but it's an active DNS record, add it
	if !found {
		cfClient := NewCloudflareClient(credentials)
		records, err := cfClient.GetDNSRecords()
		if err == nil {
			for _, record := range records {
				dnsName := strings.TrimSuffix(record.Name, "."+credentials.Domain)
				if record.Content == req.IP && dnsName == req.Name {
					// Add to config
					now := time.Now().Format(time.RFC3339)
					newServer := Server{
						UniqueID:        generateServerID(record.Name, record.Content),
						Alias:           record.Comment,
						Description:     fmt.Sprintf("Added via tag update on %s", time.Now().Format("2006-01-02")),
						FirstSeenOn:     now,
						LastActivatedOn: now,
						Type:            "A",
						Name:            record.Name,
						Content:         record.Content,
						TTL:             record.TTL,
						Proxied:         record.Proxied,
						Comment:         record.Comment,
					}
					
					if req.TagType == "account" {
						newServer.Account = req.Value
					} else if req.TagType == "container" {
						newServer.Container = req.Value
					}
					
					config.Servers = append(config.Servers, newServer)
					found = true
					break
				}
			}
		}
	}
	
	if !found {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Server not found",
		})
		return
	}
	
	// Save configuration
	if err := saveServerConfig(*environment, config); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Failed to save configuration: %v", err),
		})
		return
	}
	
	logger.Log("INFO", fmt.Sprintf("Updated %s tag to '%s' for %s (%s)", req.TagType, req.Value, req.Name, req.IP))
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Tag updated successfully",
	})
}

// Notes update handler
func updateNotesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req struct {
		IP    string `json:"ip"`
		Notes string `json:"notes"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	
	// Load configuration
	config, err := loadServerConfig(*environment)
	if err != nil || config == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Failed to load configuration",
		})
		return
	}
	
	// Update notes for all servers with this IP
	updated := false
	for i := range config.Servers {
		if config.Servers[i].Content == req.IP {
			config.Servers[i].Notes = req.Notes
			updated = true
			logger.Log("INFO", fmt.Sprintf("Updated notes for server %s (%s)", config.Servers[i].Name, req.IP))
		}
	}
	
	if !updated {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "No servers found with this IP",
		})
		return
	}
	
	// Save configuration
	if err := saveServerConfig(*environment, config); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Failed to save configuration: %v", err),
		})
		return
	}
	
	logger.Log("INFO", fmt.Sprintf("Updated notes for IP %s", req.IP))
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Notes updated successfully",
	})
}

// addTagHandler handles adding new tags
func addTagHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req struct {
		TagType string `json:"tag_type"`
		TagName string `json:"tag_name"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Invalid request format",
		})
		return
	}
	
	// Validate tag type
	if req.TagType != "account" && req.TagType != "container" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Invalid tag type",
		})
		return
	}
	
	// Validate tag name
	req.TagName = strings.TrimSpace(req.TagName)
	if req.TagName == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Tag name cannot be empty",
		})
		return
	}
	
	// Load current configuration
	config, err := loadServerConfig(*environment)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Failed to load configuration: %v", err),
		})
		return
	}
	
	// Initialize if nil
	if config == nil {
		config = &ServerConfig{
			Environment: *environment,
			Domain:      credentials.Domain,
			Servers:     []Server{},
			AvailableAccounts: []string{},
			AvailableContainers: []string{},
		}
	}
	
	// Add the new tag if it doesn't already exist
	if req.TagType == "account" {
		// Check if already exists
		exists := false
		for _, tag := range config.AvailableAccounts {
			if tag == req.TagName {
				exists = true
				break
			}
		}
		if !exists {
			config.AvailableAccounts = append(config.AvailableAccounts, req.TagName)
			sort.Strings(config.AvailableAccounts)
		}
	} else {
		// Container
		exists := false
		for _, tag := range config.AvailableContainers {
			if tag == req.TagName {
				exists = true
				break
			}
		}
		if !exists {
			config.AvailableContainers = append(config.AvailableContainers, req.TagName)
			sort.Strings(config.AvailableContainers)
		}
	}
	
	// Save configuration
	if err := saveServerConfig(*environment, config); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Failed to save configuration: %v", err),
		})
		return
	}
	
	logger.Log("INFO", fmt.Sprintf("Added new %s tag: %s", req.TagType, req.TagName))
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Added new %s: %s", req.TagType, req.TagName),
	})
}

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
	http.HandleFunc("/api/update-tag", updateTagHandler)
	http.HandleFunc("/api/update-notes", updateNotesHandler)
	http.HandleFunc("/api/add-tag", addTagHandler)
	http.HandleFunc("/api/dns/create", createDNSHandler)
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
