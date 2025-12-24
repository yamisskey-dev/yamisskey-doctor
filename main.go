package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	OK     bool    `json:"ok"`
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
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
		CPU float64 `json:"cpu"`
		Mem struct {
			Used  int64 `json:"used"`
			Total int64 `json:"total"`
		} `json:"mem"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	memPercent := 0.0
	if data.Mem.Total > 0 {
		memPercent = float64(data.Mem.Used) / float64(data.Mem.Total) * 100
	}

	return &ServerCheck{
		OK:     true,
		CPU:    data.CPU,
		Memory: memPercent,
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
		fmt.Printf("Server      OK    cpu:%.1f%% mem:%.1f%%\n", r.Server.CPU, r.Server.Memory)
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

func cmdRestore(args []string) int {
	fmt.Println("restore: not implemented yet")
	fmt.Println("")
	fmt.Println("Planned features:")
	fmt.Println("  - Download backup from R2/Linode via rclone")
	fmt.Println("  - Extract 7z archive")
	fmt.Println("  - Restore to PostgreSQL via pg_restore")
	return 0
}

func cmdVerify(args []string) int {
	fmt.Println("verify: not implemented yet")
	fmt.Println("")
	fmt.Println("Planned features:")
	fmt.Println("  - Create temporary PostgreSQL database")
	fmt.Println("  - Restore backup to temp DB")
	fmt.Println("  - Run integrity checks")
	fmt.Println("  - Drop temp DB")
	fmt.Println("  - Report results")
	return 0
}

func cmdRepair(args []string) int {
	fmt.Println("repair: not implemented yet")
	fmt.Println("")
	fmt.Println("Planned features:")
	fmt.Println("  - Delete orphan records")
	fmt.Println("  - Rebuild broken indexes")
	fmt.Println("  - Fix foreign key inconsistencies")
	return 0
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
