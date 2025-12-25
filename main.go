package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var version = "dev"

// ===== Check (API/Streaming) =====

type CheckResult struct {
	Status string       `json:"status"`
	API    *APICheck    `json:"api"`
	Meta   *MetaCheck   `json:"meta,omitempty"`
	Stream *StreamCheck `json:"stream,omitempty"`
	Stats  *StatsCheck  `json:"stats,omitempty"`
	Queue  *QueueCheck  `json:"queue,omitempty"`
	Server *ServerCheck `json:"server,omitempty"`
}

type APICheck struct {
	OK bool  `json:"ok"`
	Ms int64 `json:"ms"`
}

type MetaCheck struct {
	Version    string `json:"version"`
	Name       string `json:"name"`
	Federation bool   `json:"federation"`
}

type StatsCheck struct {
	Notes int64 `json:"notes"`
	Users int64 `json:"users"`
}

type StreamCheck struct {
	OK bool  `json:"ok"`
	Ms int64 `json:"ms"`
}

type QueueCheck struct {
	OK      bool  `json:"ok"`
	Deliver int64 `json:"deliver"`
	Inbox   int64 `json:"inbox"`
	DB      int64 `json:"db"`
	Delayed int64 `json:"delayed"`
}

type ServerCheck struct {
	OK        bool    `json:"ok"`
	CPUModel  string  `json:"cpuModel"`
	CPUCores  int     `json:"cpuCores"`
	MemTotal  int64   `json:"memTotal"`
	FSUsed    int64   `json:"fsUsed"`
	FSTotal   int64   `json:"fsTotal"`
	FSPercent float64 `json:"fsPercent"`
}

func runCheck(ctx context.Context, baseURL, token string) *CheckResult {
	result := &CheckResult{Status: "healthy"}

	// API check (/api/meta)
	start := time.Now()
	meta, err := fetchMeta(ctx, baseURL)
	elapsed := time.Since(start).Milliseconds()

	result.API = &APICheck{OK: err == nil, Ms: elapsed}

	if err != nil {
		result.Status = "unhealthy"
		return result
	}

	result.Meta = meta

	// Stats check (/api/stats)
	stats, err := fetchStats(ctx, baseURL)
	if err == nil {
		result.Stats = stats
	}

	// Streaming check (Redis health indicator)
	stream := checkStreaming(ctx, baseURL)
	result.Stream = stream
	if !stream.OK {
		result.Status = "degraded"
	}

	// 認証が必要なチェック
	if token != "" {
		// Queue check
		queue, err := fetchQueueStats(ctx, baseURL, token)
		if err == nil {
			result.Queue = queue
			if queue.Delayed > 1000 {
				result.Status = "degraded"
			}
		}

		// Server info check
		server, err := fetchServerInfo(ctx, baseURL, token)
		if err == nil {
			result.Server = server
		}
	}

	return result
}

func fetchMeta(ctx context.Context, baseURL string) (*MetaCheck, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/meta", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var data struct {
		Version               string `json:"version"`
		Name                  string `json:"name"`
		DisableGlobalTimeline bool   `json:"disableGlobalTimeline"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	return &MetaCheck{
		Version:    data.Version,
		Name:       data.Name,
		Federation: !data.DisableGlobalTimeline,
	}, nil
}

func fetchStats(ctx context.Context, baseURL string) (*StatsCheck, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/stats", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data struct {
		NotesCount int64 `json:"notesCount"`
		UsersCount int64 `json:"usersCount"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	return &StatsCheck{
		Notes: data.NotesCount,
		Users: data.UsersCount,
	}, nil
}

func checkStreaming(ctx context.Context, baseURL string) *StreamCheck {
	u, _ := url.Parse(baseURL)
	wsURL := "wss://" + u.Host + "/streaming"

	start := time.Now()

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		return &StreamCheck{OK: false, Ms: elapsed}
	}
	conn.Close()

	return &StreamCheck{OK: true, Ms: elapsed}
}

func fetchQueueStats(ctx context.Context, baseURL, token string) (*QueueCheck, error) {
	body := fmt.Sprintf(`{"i":"%s"}`, token)
	req, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/admin/queue/stats", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var data struct {
		Deliver struct {
			Waiting int64 `json:"waiting"`
			Delayed int64 `json:"delayed"`
		} `json:"deliver"`
		Inbox struct {
			Waiting int64 `json:"waiting"`
			Delayed int64 `json:"delayed"`
		} `json:"inbox"`
		DB struct {
			Waiting int64 `json:"waiting"`
			Delayed int64 `json:"delayed"`
		} `json:"db"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	return &QueueCheck{
		OK:      true,
		Deliver: data.Deliver.Waiting,
		Inbox:   data.Inbox.Waiting,
		DB:      data.DB.Waiting,
		Delayed: data.Deliver.Delayed + data.Inbox.Delayed + data.DB.Delayed,
	}, nil
}

func fetchServerInfo(ctx context.Context, baseURL, token string) (*ServerCheck, error) {
	body := fmt.Sprintf(`{"i":"%s"}`, token)
	req, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/admin/server-info", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var data struct {
		CPU struct {
			Model string `json:"model"`
			Cores int    `json:"cores"`
		} `json:"cpu"`
		Mem struct {
			Total int64 `json:"total"`
		} `json:"mem"`
		FS struct {
			Total int64 `json:"total"`
			Used  int64 `json:"used"`
		} `json:"fs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	fsPercent := 0.0
	if data.FS.Total > 0 {
		fsPercent = float64(data.FS.Used) / float64(data.FS.Total) * 100
	}

	return &ServerCheck{
		OK:        true,
		CPUModel:  data.CPU.Model,
		CPUCores:  data.CPU.Cores,
		MemTotal:  data.Mem.Total,
		FSUsed:    data.FS.Used,
		FSTotal:   data.FS.Total,
		FSPercent: fsPercent,
	}, nil
}

func printCheckText(r *CheckResult) {
	if r.API != nil {
		status := "OK"
		if !r.API.OK {
			status = "FAIL"
		}
		fmt.Printf("API         %s    %dms\n", status, r.API.Ms)
	}

	if r.Meta != nil {
		fmt.Printf("Version     OK    %s\n", r.Meta.Version)
		fmt.Printf("Name        OK    %s\n", r.Meta.Name)
		fed := "enabled"
		if !r.Meta.Federation {
			fed = "disabled"
		}
		fmt.Printf("Federation  OK    %s\n", fed)
	}

	if r.Stream != nil {
		status := "OK"
		if !r.Stream.OK {
			status = "FAIL"
		}
		fmt.Printf("Streaming   %s    %dms\n", status, r.Stream.Ms)
	}

	if r.Stats != nil {
		fmt.Printf("Stats       OK    notes:%d users:%d\n", r.Stats.Notes, r.Stats.Users)
	}

	if r.Queue != nil {
		status := "OK"
		if r.Queue.Delayed > 1000 {
			status = "WARN"
		}
		fmt.Printf("Queue       %s    deliver:%d inbox:%d db:%d delayed:%d\n",
			status, r.Queue.Deliver, r.Queue.Inbox, r.Queue.DB, r.Queue.Delayed)
	}

	if r.Server != nil {
		memGB := float64(r.Server.MemTotal) / (1024 * 1024 * 1024)
		fmt.Printf("Server      OK    %s (%d cores) mem:%.1fGB disk:%.1f%%\n",
			r.Server.CPUModel, r.Server.CPUCores, memGB, r.Server.FSPercent)
	}
}

// ===== Commands =====

func cmdCheck(args []string) int {
	var (
		format  string
		timeout int
		quiet   bool
	)

	// Simple arg parsing
	targetURL := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--format":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "-t", "--timeout":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &timeout)
				i++
			}
		case "-q", "--quiet":
			quiet = true
		default:
			if !strings.HasPrefix(args[i], "-") && targetURL == "" {
				targetURL = args[i]
			}
		}
	}

	if format == "" {
		format = "text"
	}
	if timeout == 0 {
		timeout = 5
	}

	if targetURL == "" {
		fmt.Fprintln(os.Stderr, "usage: yamisskey-doctor check <url>")
		return 2
	}

	if !strings.HasPrefix(targetURL, "http") {
		targetURL = "https://" + targetURL
	}

	token := os.Getenv("MISSKEY_TOKEN")

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	result := runCheck(ctx, targetURL, token)

	if !quiet {
		switch format {
		case "json":
			enc := json.NewEncoder(os.Stdout)
			enc.Encode(result)
		default:
			printCheckText(result)
		}
	}

	if result.Status != "healthy" {
		if result.API != nil && !result.API.OK {
			return 2
		}
		return 1
	}
	return 0
}

// ===== Restore =====

type RestoreConfig struct {
	// Storage settings
	StorageType   string // "r2" or "linode"
	R2Prefix      string
	LinodeBucket  string
	LinodePrefix  string

	// PostgreSQL settings
	PGHost     string
	PGPort     string
	PGUser     string
	PGPassword string
	PGDatabase string

	// Options
	BackupFile string // specific backup file to restore (optional)
	WorkDir    string // working directory for downloads
	DryRun     bool
	Force      bool
}

func loadRestoreConfigFromEnv() *RestoreConfig {
	cfg := &RestoreConfig{
		StorageType:  getEnvOrDefault("STORAGE_TYPE", "r2"),
		R2Prefix:     getEnvOrDefault("R2_PREFIX", "backups"),
		LinodeBucket: getEnvOrDefault("LINODE_BUCKET", "yamisskey-backup"),
		LinodePrefix: getEnvOrDefault("LINODE_PREFIX", "backups"),
		PGHost:       getEnvOrDefault("POSTGRES_HOST", "localhost"),
		PGPort:       getEnvOrDefault("POSTGRES_PORT", "5432"),
		PGUser:       getEnvOrDefault("POSTGRES_USER", "misskey"),
		PGPassword:   os.Getenv("PGPASSWORD"),
		PGDatabase:   getEnvOrDefault("POSTGRES_DB", "mk1"),
		WorkDir:      getEnvOrDefault("WORK_DIR", "/tmp/yamisskey-restore"),
	}
	return cfg
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// listBackups lists available backups from storage
func listBackups(cfg *RestoreConfig) ([]string, error) {
	var remote string
	if cfg.StorageType == "linode" {
		remote = fmt.Sprintf("linode:%s/%s", cfg.LinodeBucket, cfg.LinodePrefix)
	} else {
		remote = fmt.Sprintf("r2:%s", cfg.R2Prefix)
	}

	cmd := exec.Command("rclone", "ls", remote)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list backups: %w", err)
	}

	var backups []string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// rclone ls format: "  12345 filename.sql.7z"
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			filename := parts[len(parts)-1]
			if strings.HasSuffix(filename, ".sql.7z") {
				backups = append(backups, filename)
			}
		}
	}

	// Sort by name (which includes date) descending
	sort.Sort(sort.Reverse(sort.StringSlice(backups)))

	return backups, nil
}

// downloadBackup downloads a backup file from storage
func downloadBackup(cfg *RestoreConfig, filename string) (string, error) {
	// Create work directory
	if err := os.MkdirAll(cfg.WorkDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create work directory: %w", err)
	}

	var remote string
	if cfg.StorageType == "linode" {
		remote = fmt.Sprintf("linode:%s/%s/%s", cfg.LinodeBucket, cfg.LinodePrefix, filename)
	} else {
		remote = fmt.Sprintf("r2:%s/%s", cfg.R2Prefix, filename)
	}

	localPath := filepath.Join(cfg.WorkDir, filename)

	fmt.Printf("Downloading %s...\n", filename)
	cmd := exec.Command("rclone", "copy", "--progress", remote, cfg.WorkDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to download backup: %w", err)
	}

	// Verify file exists
	if _, err := os.Stat(localPath); err != nil {
		return "", fmt.Errorf("downloaded file not found: %w", err)
	}

	fmt.Printf("Downloaded: %s\n", localPath)
	return localPath, nil
}

// extractBackup extracts a 7z archive
func extractBackup(archivePath string) (string, error) {
	dir := filepath.Dir(archivePath)

	fmt.Printf("Extracting %s...\n", filepath.Base(archivePath))
	cmd := exec.Command("7z", "x", "-y", "-o"+dir, archivePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to extract archive: %w", err)
	}

	// The SQL file should have the same name without .7z
	sqlPath := strings.TrimSuffix(archivePath, ".7z")
	if _, err := os.Stat(sqlPath); err != nil {
		return "", fmt.Errorf("extracted SQL file not found: %w", err)
	}

	fmt.Printf("Extracted: %s\n", sqlPath)
	return sqlPath, nil
}

// restoreDatabase restores a SQL dump to PostgreSQL
func restoreDatabase(cfg *RestoreConfig, sqlPath string) error {
	fmt.Printf("Restoring to database %s@%s:%s/%s...\n",
		cfg.PGUser, cfg.PGHost, cfg.PGPort, cfg.PGDatabase)

	// Set PGPASSWORD environment variable
	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	// Use psql for SQL text dumps (pg_dump default format)
	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-f", sqlPath,
	)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to restore database: %w", err)
	}

	fmt.Println("Database restored successfully!")
	return nil
}

// cleanup removes temporary files
func cleanup(paths ...string) {
	for _, path := range paths {
		if path != "" {
			os.Remove(path)
		}
	}
}

func cmdRestore(args []string) int {
	cfg := loadRestoreConfigFromEnv()

	// Parse arguments
	var listOnly bool
	var latest bool

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-l", "--list":
			listOnly = true
		case "--latest":
			latest = true
		case "-s", "--storage":
			if i+1 < len(args) {
				cfg.StorageType = args[i+1]
				i++
			}
		case "-f", "--file":
			if i+1 < len(args) {
				cfg.BackupFile = args[i+1]
				i++
			}
		case "-d", "--database":
			if i+1 < len(args) {
				cfg.PGDatabase = args[i+1]
				i++
			}
		case "--dry-run":
			cfg.DryRun = true
		case "--force":
			cfg.Force = true
		case "-h", "--help":
			printRestoreUsage()
			return 0
		}
	}

	// Check required tools
	for _, tool := range []string{"rclone", "7z", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			fmt.Fprintf(os.Stderr, "Error: required tool '%s' not found in PATH\n", tool)
			return 1
		}
	}

	// List backups
	fmt.Printf("Fetching backup list from %s...\n", cfg.StorageType)
	backups, err := listBackups(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	if len(backups) == 0 {
		fmt.Println("No backups found.")
		return 0
	}

	// List only mode
	if listOnly {
		fmt.Println("\nAvailable backups:")
		for i, b := range backups {
			fmt.Printf("  [%d] %s\n", i+1, b)
		}
		return 0
	}

	// Select backup file
	var selectedBackup string
	if cfg.BackupFile != "" {
		selectedBackup = cfg.BackupFile
	} else if latest {
		selectedBackup = backups[0]
		fmt.Printf("Selected latest backup: %s\n", selectedBackup)
	} else {
		// Interactive selection
		fmt.Println("\nAvailable backups:")
		for i, b := range backups {
			fmt.Printf("  [%d] %s\n", i+1, b)
		}
		fmt.Print("\nSelect backup number (or 'q' to quit): ")

		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "q" || input == "" {
			fmt.Println("Cancelled.")
			return 0
		}

		var num int
		if _, err := fmt.Sscanf(input, "%d", &num); err != nil || num < 1 || num > len(backups) {
			fmt.Fprintf(os.Stderr, "Invalid selection: %s\n", input)
			return 1
		}
		selectedBackup = backups[num-1]
	}

	// Confirmation
	if !cfg.Force && !cfg.DryRun {
		fmt.Printf("\n⚠️  WARNING: This will restore backup to database '%s'\n", cfg.PGDatabase)
		fmt.Printf("   Backup: %s\n", selectedBackup)
		fmt.Printf("   Host: %s:%s\n", cfg.PGHost, cfg.PGPort)
		fmt.Print("\nType 'yes' to continue: ")

		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input != "yes" {
			fmt.Println("Cancelled.")
			return 0
		}
	}

	if cfg.DryRun {
		fmt.Println("\n[DRY RUN] Would execute:")
		fmt.Printf("  1. Download: %s\n", selectedBackup)
		fmt.Printf("  2. Extract: %s\n", strings.TrimSuffix(selectedBackup, ".7z"))
		fmt.Printf("  3. Restore to: %s@%s:%s/%s\n", cfg.PGUser, cfg.PGHost, cfg.PGPort, cfg.PGDatabase)
		return 0
	}

	// Execute restore
	fmt.Println()

	// 1. Download
	archivePath, err := downloadBackup(cfg, selectedBackup)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer cleanup(archivePath)

	// 2. Extract
	sqlPath, err := extractBackup(archivePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer cleanup(sqlPath)

	// 3. Restore
	if err := restoreDatabase(cfg, sqlPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	fmt.Println("\n✅ Restore completed successfully!")
	return 0
}

func printRestoreUsage() {
	fmt.Println("Usage: yamisskey-doctor restore [options]")
	fmt.Println("")
	fmt.Println("Options:")
	fmt.Println("  -l, --list       List available backups")
	fmt.Println("  --latest         Restore the latest backup")
	fmt.Println("  -s, --storage    Storage type: r2 or linode (default: r2)")
	fmt.Println("  -f, --file       Specific backup file to restore")
	fmt.Println("  -d, --database   Target database name")
	fmt.Println("  --dry-run        Show what would be done without executing")
	fmt.Println("  --force          Skip confirmation prompt")
	fmt.Println("")
	fmt.Println("Environment variables:")
	fmt.Println("  STORAGE_TYPE     Storage type (r2/linode)")
	fmt.Println("  R2_PREFIX        R2 bucket prefix (default: backups)")
	fmt.Println("  LINODE_BUCKET    Linode bucket name (default: yamisskey-backup)")
	fmt.Println("  LINODE_PREFIX    Linode prefix (default: backups)")
	fmt.Println("  POSTGRES_HOST    PostgreSQL host (default: localhost)")
	fmt.Println("  POSTGRES_PORT    PostgreSQL port (default: 5432)")
	fmt.Println("  POSTGRES_USER    PostgreSQL user (default: misskey)")
	fmt.Println("  POSTGRES_DB      PostgreSQL database (default: mk1)")
	fmt.Println("  PGPASSWORD       PostgreSQL password")
	fmt.Println("  WORK_DIR         Working directory for downloads")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  yamisskey-doctor restore --list")
	fmt.Println("  yamisskey-doctor restore --latest")
	fmt.Println("  yamisskey-doctor restore --storage linode --latest")
	fmt.Println("  yamisskey-doctor restore --file mk1_2025-01-01_03-00.sql.7z")
}

// ===== Verify =====

type VerifyResult struct {
	BackupFile  string        `json:"backupFile"`
	OK          bool          `json:"ok"`
	DownloadOK  bool          `json:"downloadOk"`
	ExtractOK   bool          `json:"extractOk"`
	RestoreOK   bool          `json:"restoreOk"`
	IntegrityOK bool          `json:"integrityOk"`
	Tables      int           `json:"tables"`
	Error       string        `json:"error,omitempty"`
	Checks      []VerifyCheck `json:"checks,omitempty"`
}

type VerifyCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// createTempDatabase creates a temporary database for verification
func createTempDatabase(cfg *RestoreConfig, tempDBName string) error {
	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	// Connect to 'postgres' database to create new database
	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", "postgres",
		"-c", fmt.Sprintf("CREATE DATABASE %s", tempDBName),
	)
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create temp database: %s", string(output))
	}
	return nil
}

// dropTempDatabase drops the temporary database
func dropTempDatabase(cfg *RestoreConfig, tempDBName string) error {
	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	// Terminate connections first
	terminateCmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", "postgres",
		"-c", fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", tempDBName),
	)
	terminateCmd.Env = env
	terminateCmd.Run() // Ignore errors

	// Drop database
	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", "postgres",
		"-c", fmt.Sprintf("DROP DATABASE IF EXISTS %s", tempDBName),
	)
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to drop temp database: %s", string(output))
	}
	return nil
}

// restoreToTempDatabase restores SQL to temporary database
func restoreToTempDatabase(cfg *RestoreConfig, tempDBName, sqlPath string) error {
	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", tempDBName,
		"-f", sqlPath,
		"-v", "ON_ERROR_STOP=1",
	)
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's just warnings vs real errors
		if strings.Contains(string(output), "ERROR:") {
			return fmt.Errorf("restore failed: %s", string(output))
		}
	}
	return nil
}

// runIntegrityChecks runs basic integrity checks on the database
func runIntegrityChecks(cfg *RestoreConfig, tempDBName string) ([]VerifyCheck, int, error) {
	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	var checks []VerifyCheck
	var tableCount int

	// Check 1: Count tables
	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", tempDBName,
		"-t", "-c",
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public'",
	)
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count tables: %w", err)
	}
	fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &tableCount)
	checks = append(checks, VerifyCheck{
		Name:   "table_count",
		OK:     tableCount > 0,
		Detail: fmt.Sprintf("%d tables", tableCount),
	})

	// Check 2: Verify critical Misskey tables exist
	criticalTables := []string{"user", "note", "meta", "instance"}
	for _, table := range criticalTables {
		cmd := exec.Command("psql",
			"-h", cfg.PGHost,
			"-p", cfg.PGPort,
			"-U", cfg.PGUser,
			"-d", tempDBName,
			"-t", "-c",
			fmt.Sprintf("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = '%s'", table),
		)
		cmd.Env = env
		output, _ := cmd.Output()
		count := 0
		fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &count)
		checks = append(checks, VerifyCheck{
			Name:   fmt.Sprintf("table_%s", table),
			OK:     count > 0,
			Detail: fmt.Sprintf("table '%s' exists: %v", table, count > 0),
		})
	}

	// Check 3: Count users
	cmd = exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", tempDBName,
		"-t", "-c",
		`SELECT COUNT(*) FROM "user"`,
	)
	cmd.Env = env
	output, err = cmd.Output()
	if err == nil {
		userCount := 0
		fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &userCount)
		checks = append(checks, VerifyCheck{
			Name:   "user_count",
			OK:     true,
			Detail: fmt.Sprintf("%d users", userCount),
		})
	}

	// Check 4: Count notes
	cmd = exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", tempDBName,
		"-t", "-c",
		"SELECT COUNT(*) FROM note",
	)
	cmd.Env = env
	output, err = cmd.Output()
	if err == nil {
		noteCount := 0
		fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &noteCount)
		checks = append(checks, VerifyCheck{
			Name:   "note_count",
			OK:     true,
			Detail: fmt.Sprintf("%d notes", noteCount),
		})
	}

	// Check 5: Foreign key integrity (sample check)
	cmd = exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", tempDBName,
		"-t", "-c",
		`SELECT COUNT(*) FROM note WHERE "userId" NOT IN (SELECT id FROM "user")`,
	)
	cmd.Env = env
	output, err = cmd.Output()
	if err == nil {
		orphanNotes := 0
		fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &orphanNotes)
		checks = append(checks, VerifyCheck{
			Name:   "orphan_notes",
			OK:     orphanNotes == 0,
			Detail: fmt.Sprintf("%d orphan notes", orphanNotes),
		})
	}

	return checks, tableCount, nil
}

func cmdVerify(args []string) int {
	cfg := loadRestoreConfigFromEnv()

	// Parse arguments
	var (
		listOnly  bool
		latest    bool
		format    string
		localFile string // Local SQL file path (skip download/extract)
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-l", "--list":
			listOnly = true
		case "--latest":
			latest = true
		case "-s", "--storage":
			if i+1 < len(args) {
				cfg.StorageType = args[i+1]
				i++
			}
		case "-f", "--file":
			if i+1 < len(args) {
				cfg.BackupFile = args[i+1]
				i++
			}
		case "--local":
			if i+1 < len(args) {
				localFile = args[i+1]
				i++
			}
		case "--format":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "-h", "--help":
			printVerifyUsage()
			return 0
		}
	}

	// Local file mode - skip rclone/7z requirements
	if localFile != "" {
		return cmdVerifyLocal(cfg, localFile, format)
	}

	// Check required tools
	for _, tool := range []string{"rclone", "7z", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			fmt.Fprintf(os.Stderr, "Error: required tool '%s' not found in PATH\n", tool)
			return 1
		}
	}

	// List backups
	fmt.Printf("Fetching backup list from %s...\n", cfg.StorageType)
	backups, err := listBackups(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	if len(backups) == 0 {
		fmt.Println("No backups found.")
		return 0
	}

	// List only mode
	if listOnly {
		fmt.Println("\nAvailable backups:")
		for i, b := range backups {
			fmt.Printf("  [%d] %s\n", i+1, b)
		}
		return 0
	}

	// Select backup file
	var selectedBackup string
	if cfg.BackupFile != "" {
		selectedBackup = cfg.BackupFile
	} else if latest {
		selectedBackup = backups[0]
		fmt.Printf("Selected latest backup: %s\n", selectedBackup)
	} else {
		// Interactive selection
		fmt.Println("\nAvailable backups:")
		for i, b := range backups {
			fmt.Printf("  [%d] %s\n", i+1, b)
		}
		fmt.Print("\nSelect backup number (or 'q' to quit): ")

		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "q" || input == "" {
			fmt.Println("Cancelled.")
			return 0
		}

		var num int
		if _, err := fmt.Sscanf(input, "%d", &num); err != nil || num < 1 || num > len(backups) {
			fmt.Fprintf(os.Stderr, "Invalid selection: %s\n", input)
			return 1
		}
		selectedBackup = backups[num-1]
	}

	// Generate temp database name
	tempDBName := fmt.Sprintf("yamisskey_verify_%d", time.Now().Unix())

	result := VerifyResult{
		BackupFile: selectedBackup,
	}

	fmt.Println()
	fmt.Printf("Verifying backup: %s\n", selectedBackup)
	fmt.Printf("Temp database: %s\n", tempDBName)
	fmt.Println()

	// Ensure cleanup on exit
	defer func() {
		fmt.Printf("Cleaning up temp database %s...\n", tempDBName)
		dropTempDatabase(cfg, tempDBName)
	}()

	// Step 1: Download
	fmt.Println("[1/4] Downloading backup...")
	archivePath, err := downloadBackup(cfg, selectedBackup)
	if err != nil {
		result.Error = fmt.Sprintf("Download failed: %v", err)
		printVerifyResult(&result, format)
		return 1
	}
	defer cleanup(archivePath)
	result.DownloadOK = true
	fmt.Println("      Download OK")

	// Step 2: Extract
	fmt.Println("[2/4] Extracting archive...")
	sqlPath, err := extractBackup(archivePath)
	if err != nil {
		result.Error = fmt.Sprintf("Extract failed: %v", err)
		printVerifyResult(&result, format)
		return 1
	}
	defer cleanup(sqlPath)
	result.ExtractOK = true
	fmt.Println("      Extract OK")

	// Step 3: Create temp DB and restore
	fmt.Println("[3/4] Creating temp database and restoring...")
	if err := createTempDatabase(cfg, tempDBName); err != nil {
		result.Error = fmt.Sprintf("Create temp DB failed: %v", err)
		printVerifyResult(&result, format)
		return 1
	}

	if err := restoreToTempDatabase(cfg, tempDBName, sqlPath); err != nil {
		result.Error = fmt.Sprintf("Restore failed: %v", err)
		printVerifyResult(&result, format)
		return 1
	}
	result.RestoreOK = true
	fmt.Println("      Restore OK")

	// Step 4: Run integrity checks
	fmt.Println("[4/4] Running integrity checks...")
	checks, tableCount, err := runIntegrityChecks(cfg, tempDBName)
	if err != nil {
		result.Error = fmt.Sprintf("Integrity check failed: %v", err)
		printVerifyResult(&result, format)
		return 1
	}
	result.Checks = checks
	result.Tables = tableCount

	// Determine overall integrity
	result.IntegrityOK = true
	for _, check := range checks {
		if !check.OK && strings.HasPrefix(check.Name, "table_") {
			result.IntegrityOK = false
			break
		}
	}

	result.OK = result.DownloadOK && result.ExtractOK && result.RestoreOK && result.IntegrityOK
	fmt.Println("      Integrity checks complete")

	printVerifyResult(&result, format)

	if result.OK {
		return 0
	}
	return 1
}

// cmdVerifyLocal verifies a local SQL file without downloading
func cmdVerifyLocal(cfg *RestoreConfig, sqlPath string, format string) int {
	// Check required tools (only psql needed for local mode)
	if _, err := exec.LookPath("psql"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: required tool 'psql' not found in PATH\n")
		return 1
	}

	// Check if file exists
	if _, err := os.Stat(sqlPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: file not found: %s\n", sqlPath)
		return 1
	}

	// Generate temp database name
	tempDBName := fmt.Sprintf("yamisskey_verify_%d", time.Now().Unix())

	result := VerifyResult{
		BackupFile: sqlPath,
		DownloadOK: true, // N/A for local
		ExtractOK:  true, // N/A for local
	}

	fmt.Printf("Verifying local SQL file: %s\n", sqlPath)
	fmt.Printf("Temp database: %s\n", tempDBName)
	fmt.Println()

	// Ensure cleanup on exit
	defer func() {
		fmt.Printf("Cleaning up temp database %s...\n", tempDBName)
		dropTempDatabase(cfg, tempDBName)
	}()

	// Step 1: Create temp DB and restore
	fmt.Println("[1/2] Creating temp database and restoring...")
	if err := createTempDatabase(cfg, tempDBName); err != nil {
		result.Error = fmt.Sprintf("Create temp DB failed: %v", err)
		printVerifyResult(&result, format)
		return 1
	}

	if err := restoreToTempDatabase(cfg, tempDBName, sqlPath); err != nil {
		result.Error = fmt.Sprintf("Restore failed: %v", err)
		printVerifyResult(&result, format)
		return 1
	}
	result.RestoreOK = true
	fmt.Println("      Restore OK")

	// Step 2: Run integrity checks
	fmt.Println("[2/2] Running integrity checks...")
	checks, tableCount, err := runIntegrityChecks(cfg, tempDBName)
	if err != nil {
		result.Error = fmt.Sprintf("Integrity check failed: %v", err)
		printVerifyResult(&result, format)
		return 1
	}
	result.Checks = checks
	result.Tables = tableCount

	// Determine overall integrity
	result.IntegrityOK = true
	for _, check := range checks {
		if !check.OK && strings.HasPrefix(check.Name, "table_") {
			result.IntegrityOK = false
			break
		}
	}

	result.OK = result.RestoreOK && result.IntegrityOK
	fmt.Println("      Integrity checks complete")

	printVerifyResult(&result, format)

	if result.OK {
		return 0
	}
	return 1
}

func printVerifyResult(result *VerifyResult, format string) {
	fmt.Println()

	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		return
	}

	// Text format
	status := "PASS"
	if !result.OK {
		status = "FAIL"
	}

	fmt.Printf("=== Verification Result: %s ===\n", status)
	fmt.Printf("Backup:     %s\n", result.BackupFile)
	fmt.Printf("Download:   %s\n", boolToStatus(result.DownloadOK))
	fmt.Printf("Extract:    %s\n", boolToStatus(result.ExtractOK))
	fmt.Printf("Restore:    %s\n", boolToStatus(result.RestoreOK))
	fmt.Printf("Integrity:  %s\n", boolToStatus(result.IntegrityOK))

	if result.Error != "" {
		fmt.Printf("Error:      %s\n", result.Error)
	}

	if len(result.Checks) > 0 {
		fmt.Println("\nIntegrity Checks:")
		for _, check := range result.Checks {
			status := "OK"
			if !check.OK {
				status = "FAIL"
			}
			fmt.Printf("  %-15s %s  %s\n", check.Name, status, check.Detail)
		}
	}

	fmt.Println()
	if result.OK {
		fmt.Println("Backup is valid and can be restored.")
	} else {
		fmt.Println("Backup verification failed.")
	}
}

func boolToStatus(b bool) string {
	if b {
		return "OK"
	}
	return "FAIL"
}

func printVerifyUsage() {
	fmt.Println("Usage: yamisskey-doctor verify [options]")
	fmt.Println("")
	fmt.Println("Verify that a backup can be successfully restored.")
	fmt.Println("")
	fmt.Println("Options:")
	fmt.Println("  -l, --list       List available backups")
	fmt.Println("  --latest         Verify the latest backup")
	fmt.Println("  -s, --storage    Storage type: r2 or linode (default: r2)")
	fmt.Println("  -f, --file       Specific backup file to verify")
	fmt.Println("  --local          Verify a local SQL file (skip download/extract)")
	fmt.Println("  --format         Output format: text or json (default: text)")
	fmt.Println("")
	fmt.Println("Environment variables: (same as restore command)")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  yamisskey-doctor verify --latest")
	fmt.Println("  yamisskey-doctor verify --file mk1_2025-01-01_03-00.sql.7z")
	fmt.Println("  yamisskey-doctor verify --local /path/to/backup.sql")
	fmt.Println("  yamisskey-doctor verify --latest --format json")
}

// ===== Repair =====

type RepairResult struct {
	OK      bool          `json:"ok"`
	DryRun  bool          `json:"dryRun"`
	Repairs []RepairCheck `json:"repairs"`
	Error   string        `json:"error,omitempty"`
}

type RepairCheck struct {
	Name    string `json:"name"`
	Found   int    `json:"found"`
	Fixed   int    `json:"fixed"`
	Skipped bool   `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

// repairOrphanNotes finds and optionally deletes notes with missing users
func repairOrphanNotes(cfg *RestoreConfig, dryRun bool) RepairCheck {
	check := RepairCheck{Name: "orphan_notes"}

	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	// Count orphan notes
	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-t", "-c",
		`SELECT COUNT(*) FROM note WHERE "userId" NOT IN (SELECT id FROM "user")`,
	)
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		check.Error = fmt.Sprintf("failed to count: %v", err)
		return check
	}
	fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &check.Found)

	if check.Found == 0 {
		return check
	}

	if dryRun {
		check.Skipped = true
		return check
	}

	// Delete orphan notes
	cmd = exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-t", "-c",
		`DELETE FROM note WHERE "userId" NOT IN (SELECT id FROM "user")`,
	)
	cmd.Env = env
	_, err = cmd.Output()
	if err != nil {
		check.Error = fmt.Sprintf("failed to delete: %v", err)
		return check
	}
	check.Fixed = check.Found

	return check
}

// repairOrphanReactions finds and optionally deletes reactions with missing notes
func repairOrphanReactions(cfg *RestoreConfig, dryRun bool) RepairCheck {
	check := RepairCheck{Name: "orphan_reactions"}

	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	// Count orphan reactions
	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-t", "-c",
		`SELECT COUNT(*) FROM note_reaction WHERE "noteId" NOT IN (SELECT id FROM note)`,
	)
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		check.Error = fmt.Sprintf("failed to count: %v", err)
		return check
	}
	fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &check.Found)

	if check.Found == 0 {
		return check
	}

	if dryRun {
		check.Skipped = true
		return check
	}

	// Delete orphan reactions
	cmd = exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-t", "-c",
		`DELETE FROM note_reaction WHERE "noteId" NOT IN (SELECT id FROM note)`,
	)
	cmd.Env = env
	_, err = cmd.Output()
	if err != nil {
		check.Error = fmt.Sprintf("failed to delete: %v", err)
		return check
	}
	check.Fixed = check.Found

	return check
}

// repairOrphanNotifications finds and optionally deletes notifications with missing users
func repairOrphanNotifications(cfg *RestoreConfig, dryRun bool) RepairCheck {
	check := RepairCheck{Name: "orphan_notifications"}

	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	// Count orphan notifications
	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-t", "-c",
		`SELECT COUNT(*) FROM notification WHERE "notifieeId" NOT IN (SELECT id FROM "user")`,
	)
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		check.Error = fmt.Sprintf("failed to count: %v", err)
		return check
	}
	fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &check.Found)

	if check.Found == 0 {
		return check
	}

	if dryRun {
		check.Skipped = true
		return check
	}

	// Delete orphan notifications
	cmd = exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-t", "-c",
		`DELETE FROM notification WHERE "notifieeId" NOT IN (SELECT id FROM "user")`,
	)
	cmd.Env = env
	_, err = cmd.Output()
	if err != nil {
		check.Error = fmt.Sprintf("failed to delete: %v", err)
		return check
	}
	check.Fixed = check.Found

	return check
}

// repairOrphanDriveFiles finds and optionally deletes drive files with missing users
func repairOrphanDriveFiles(cfg *RestoreConfig, dryRun bool) RepairCheck {
	check := RepairCheck{Name: "orphan_drive_files"}

	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	// Count orphan drive files
	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-t", "-c",
		`SELECT COUNT(*) FROM drive_file WHERE "userId" IS NOT NULL AND "userId" NOT IN (SELECT id FROM "user")`,
	)
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		check.Error = fmt.Sprintf("failed to count: %v", err)
		return check
	}
	fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &check.Found)

	if check.Found == 0 {
		return check
	}

	if dryRun {
		check.Skipped = true
		return check
	}

	// Delete orphan drive files
	cmd = exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-t", "-c",
		`DELETE FROM drive_file WHERE "userId" IS NOT NULL AND "userId" NOT IN (SELECT id FROM "user")`,
	)
	cmd.Env = env
	_, err = cmd.Output()
	if err != nil {
		check.Error = fmt.Sprintf("failed to delete: %v", err)
		return check
	}
	check.Fixed = check.Found

	return check
}

// reindexDatabase runs REINDEX on the database
func reindexDatabase(cfg *RestoreConfig, dryRun bool) RepairCheck {
	check := RepairCheck{Name: "reindex"}

	if dryRun {
		check.Found = 1
		check.Skipped = true
		return check
	}

	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-c", "REINDEX DATABASE CONCURRENTLY "+cfg.PGDatabase,
	)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		// REINDEX CONCURRENTLY requires PostgreSQL 12+, try without CONCURRENTLY
		cmd = exec.Command("psql",
			"-h", cfg.PGHost,
			"-p", cfg.PGPort,
			"-U", cfg.PGUser,
			"-d", cfg.PGDatabase,
			"-c", "REINDEX DATABASE "+cfg.PGDatabase,
		)
		cmd.Env = env
		output, err = cmd.CombinedOutput()
		if err != nil {
			check.Error = fmt.Sprintf("failed: %s", string(output))
			return check
		}
	}
	check.Found = 1
	check.Fixed = 1

	return check
}

// vacuumAnalyze runs VACUUM ANALYZE on the database
func vacuumAnalyze(cfg *RestoreConfig, dryRun bool) RepairCheck {
	check := RepairCheck{Name: "vacuum_analyze"}

	if dryRun {
		check.Found = 1
		check.Skipped = true
		return check
	}

	env := os.Environ()
	if cfg.PGPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.PGPassword)
	}

	cmd := exec.Command("psql",
		"-h", cfg.PGHost,
		"-p", cfg.PGPort,
		"-U", cfg.PGUser,
		"-d", cfg.PGDatabase,
		"-c", "VACUUM ANALYZE",
	)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		check.Error = fmt.Sprintf("failed: %s", string(output))
		return check
	}
	check.Found = 1
	check.Fixed = 1

	return check
}

func cmdRepair(args []string) int {
	cfg := loadRestoreConfigFromEnv()

	// Parse arguments
	var (
		dryRun     bool
		force      bool
		format     string
		reindex    bool
		vacuum     bool
		orphansOnly bool
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--force":
			force = true
		case "--format":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "-d", "--database":
			if i+1 < len(args) {
				cfg.PGDatabase = args[i+1]
				i++
			}
		case "--reindex":
			reindex = true
		case "--vacuum":
			vacuum = true
		case "--orphans":
			orphansOnly = true
		case "-h", "--help":
			printRepairUsage()
			return 0
		}
	}

	// Check required tools
	if _, err := exec.LookPath("psql"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: required tool 'psql' not found in PATH\n")
		return 1
	}

	// Confirmation
	if !force && !dryRun {
		fmt.Printf("⚠️  WARNING: This will modify database '%s'\n", cfg.PGDatabase)
		fmt.Printf("   Host: %s:%s\n", cfg.PGHost, cfg.PGPort)
		fmt.Println("\n   Use --dry-run to preview changes without modifying data.")
		fmt.Print("\nType 'yes' to continue: ")

		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input != "yes" {
			fmt.Println("Cancelled.")
			return 0
		}
	}

	result := RepairResult{
		DryRun: dryRun,
	}

	if dryRun {
		fmt.Println("[DRY RUN] Checking for issues (no changes will be made)...")
	} else {
		fmt.Println("Running repairs...")
	}
	fmt.Println()

	// Run orphan repairs (always unless only reindex/vacuum specified)
	if !reindex && !vacuum || orphansOnly {
		fmt.Println("Checking orphan records...")

		check := repairOrphanNotes(cfg, dryRun)
		result.Repairs = append(result.Repairs, check)
		printRepairCheck(check, dryRun)

		check = repairOrphanReactions(cfg, dryRun)
		result.Repairs = append(result.Repairs, check)
		printRepairCheck(check, dryRun)

		check = repairOrphanNotifications(cfg, dryRun)
		result.Repairs = append(result.Repairs, check)
		printRepairCheck(check, dryRun)

		check = repairOrphanDriveFiles(cfg, dryRun)
		result.Repairs = append(result.Repairs, check)
		printRepairCheck(check, dryRun)
	}

	// Reindex
	if reindex || (!orphansOnly && !vacuum) {
		fmt.Println("\nRebuilding indexes...")
		check := reindexDatabase(cfg, dryRun)
		result.Repairs = append(result.Repairs, check)
		printRepairCheck(check, dryRun)
	}

	// Vacuum
	if vacuum || (!orphansOnly && !reindex) {
		fmt.Println("\nRunning VACUUM ANALYZE...")
		check := vacuumAnalyze(cfg, dryRun)
		result.Repairs = append(result.Repairs, check)
		printRepairCheck(check, dryRun)
	}

	// Determine overall success
	result.OK = true
	for _, repair := range result.Repairs {
		if repair.Error != "" {
			result.OK = false
			break
		}
	}

	// Print summary
	fmt.Println()
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
	} else {
		printRepairSummary(&result)
	}

	if result.OK {
		return 0
	}
	return 1
}

func printRepairCheck(check RepairCheck, dryRun bool) {
	status := "OK"
	if check.Error != "" {
		status = "ERROR"
	}

	if check.Found == 0 {
		fmt.Printf("  %-22s %s  (none found)\n", check.Name, status)
	} else if check.Skipped {
		fmt.Printf("  %-22s %s  (found %d, would fix)\n", check.Name, status, check.Found)
	} else {
		fmt.Printf("  %-22s %s  (fixed %d/%d)\n", check.Name, status, check.Fixed, check.Found)
	}

	if check.Error != "" {
		fmt.Printf("      Error: %s\n", check.Error)
	}
}

func printRepairSummary(result *RepairResult) {
	if result.DryRun {
		fmt.Println("=== Dry Run Summary ===")
	} else {
		fmt.Println("=== Repair Summary ===")
	}

	totalFound := 0
	totalFixed := 0
	errors := 0

	for _, repair := range result.Repairs {
		totalFound += repair.Found
		totalFixed += repair.Fixed
		if repair.Error != "" {
			errors++
		}
	}

	fmt.Printf("Issues found:  %d\n", totalFound)
	if result.DryRun {
		fmt.Printf("Would fix:     %d\n", totalFound)
	} else {
		fmt.Printf("Issues fixed:  %d\n", totalFixed)
	}
	if errors > 0 {
		fmt.Printf("Errors:        %d\n", errors)
	}

	fmt.Println()
	if result.OK {
		if result.DryRun {
			fmt.Println("Dry run completed. Use without --dry-run to apply fixes.")
		} else {
			fmt.Println("Repair completed successfully.")
		}
	} else {
		fmt.Println("Repair completed with errors.")
	}
}

func printRepairUsage() {
	fmt.Println("Usage: yamisskey-doctor repair [options]")
	fmt.Println("")
	fmt.Println("Repair database inconsistencies.")
	fmt.Println("")
	fmt.Println("Options:")
	fmt.Println("  --dry-run        Preview changes without modifying data")
	fmt.Println("  --force          Skip confirmation prompt")
	fmt.Println("  -d, --database   Target database name")
	fmt.Println("  --format         Output format: text or json (default: text)")
	fmt.Println("  --orphans        Only fix orphan records")
	fmt.Println("  --reindex        Only rebuild indexes")
	fmt.Println("  --vacuum         Only run VACUUM ANALYZE")
	fmt.Println("")
	fmt.Println("Repairs performed:")
	fmt.Println("  - Delete orphan notes (notes with missing users)")
	fmt.Println("  - Delete orphan reactions (reactions on missing notes)")
	fmt.Println("  - Delete orphan notifications (notifications for missing users)")
	fmt.Println("  - Delete orphan drive files (files owned by missing users)")
	fmt.Println("  - Rebuild database indexes (REINDEX)")
	fmt.Println("  - Optimize database (VACUUM ANALYZE)")
	fmt.Println("")
	fmt.Println("Environment variables: (same as restore command)")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  yamisskey-doctor repair --dry-run")
	fmt.Println("  yamisskey-doctor repair --force")
	fmt.Println("  yamisskey-doctor repair --orphans --dry-run")
	fmt.Println("  yamisskey-doctor repair --reindex --vacuum")
}

func printUsage() {
	fmt.Println("yamisskey-doctor - Misskey instance diagnostics and repair tool")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  yamisskey-doctor <command> [options]")
	fmt.Println("")
	fmt.Println("Commands:")
	fmt.Println("  check    Check Misskey API/Streaming health")
	fmt.Println("  restore  Restore database from backup")
	fmt.Println("  verify   Verify backup can be restored")
	fmt.Println("  repair   Repair database inconsistencies")
	fmt.Println("  version  Show version")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  yamisskey-doctor check example.com")
	fmt.Println("  yamisskey-doctor check --format json example.com")
	fmt.Println("  MISSKEY_TOKEN=xxx yamisskey-doctor check example.com")
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var exitCode int

	switch cmd {
	case "check":
		exitCode = cmdCheck(args)
	case "restore":
		exitCode = cmdRestore(args)
	case "verify":
		exitCode = cmdVerify(args)
	case "repair":
		exitCode = cmdRepair(args)
	case "version", "--version", "-v":
		fmt.Println(version)
		exitCode = 0
	case "help", "--help", "-h":
		printUsage()
		exitCode = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		exitCode = 2
	}

	os.Exit(exitCode)
}
