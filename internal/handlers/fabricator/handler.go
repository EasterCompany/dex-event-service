package fabricator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/agent"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/EasterCompany/dex-event-service/internal/model"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

const (
	HandlerName         = "fabricator-handler"
	FabricatorProcessID = "system-fabricator"
)

type FabricatorHandler struct {
	agent.BaseAgent
	Config        agent.AgentConfig
	DiscordClient *discord.Client
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
}

func NewFabricatorHandler(redis *redis.Client, modelClient *model.Client, discord *discord.Client) *FabricatorHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &FabricatorHandler{
		BaseAgent: agent.BaseAgent{
			RedisClient: redis,
			ModelClient: modelClient,
			ChatManager: utils.NewChatContextManager(redis),
		},
		Config: agent.AgentConfig{
			Name:      "Fabricator",
			ProcessID: FabricatorProcessID,
			Models: map[string]string{
				"review":    "dex-fabricator-review",
				"issue":     "dex-fabricator-issue",
				"construct": "dex-fabricator-construct",
				"reporter":  "dex-fabricator-reporter",
			},
			ProtocolAliases: map[string]string{
				"review":    "Review",
				"issue":     "Issue",
				"construct": "Construct",
				"reporter":  "Reporter",
			},
			Cooldowns: map[string]int{
				"review":    300, // 5 minutes
				"issue":     300, // 5 minutes
				"construct": 300, // 5 minutes
				"reporter":  300, // 5 minutes
			},
			IdleRequirement: 300,
		},
		DiscordClient: discord,
		ctx:           ctx,
		cancel:        cancel,
	}
}

func (h *FabricatorHandler) Init(ctx context.Context) error {
	h.stopChan = make(chan struct{})
	go h.runWorker()
	log.Printf("[%s] Background worker started.", HandlerName)
	return nil
}

func (h *FabricatorHandler) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.stopChan != nil {
		close(h.stopChan)
	}
	utils.ClearProcess(context.Background(), h.RedisClient, h.DiscordClient, h.Config.ProcessID)
	return nil
}

func (h *FabricatorHandler) GetConfig() agent.AgentConfig {
	return h.Config
}

func (h *FabricatorHandler) Run(ctx context.Context) ([]agent.AnalysisResult, string, error) {
	go h.PerformWaterfall(ctx)
	return nil, "", nil
}

func (h *FabricatorHandler) runWorker() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopChan:
			return
		case <-ticker.C:
			if err := h.verifySafety(); err != nil {
				log.Printf("[%s] Safety check FAILED: %v", HandlerName, err)
				continue
			}
			h.checkAndFabricate()
		}
	}
}

func (h *FabricatorHandler) verifySafety() error {
	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")

	gitDir := filepath.Join(workingDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return fmt.Errorf("root repository not found at ~/EasterCompany/.git")
	}

	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = workingDir
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "EasterCompany/EasterCompany") {
		return fmt.Errorf("invalid repository origin")
	}

	cmd = exec.Command("gh", "auth", "status")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("github cli (gh) is not configured")
	}

	return nil
}

func (h *FabricatorHandler) checkAndFabricate() {
	ctx := h.ctx
	if utils.IsSystemPaused(ctx, h.RedisClient) {
		return
	}

	now := time.Now().Unix()
	lastRun, _ := h.RedisClient.Get(ctx, "fabricator:last_run:review").Int64()
	if now-lastRun < int64(h.Config.Cooldowns["review"]) {
		return
	}

	if h.IsActuallyBusy(ctx, h.Config.ProcessID) {
		return
	}

	go h.PerformWaterfall(ctx)
}

func (h *FabricatorHandler) PerformWaterfall(ctx context.Context) {
	if err := h.verifySafety(); err != nil {
		log.Printf("[%s] Aborting waterfall due to safety check failure: %v", HandlerName, err)

		// Report failure to front-end (cooldown 1h)
		cooldownKey := "fabricator:safety_alert_cooldown"
		if h.RedisClient.Get(ctx, cooldownKey).Val() == "" {
			payload := map[string]interface{}{
				"title":    "Fabricator Safety Alert",
				"body":     fmt.Sprintf("Fabricator disabled: %v. Please ensure root repository (~/EasterCompany/.git) and GitHub CLI are correctly configured.", err),
				"category": "error",
				"priority": "high",
				"type":     "system.notification.generated",
			}
			_, _ = utils.SendEvent(ctx, h.RedisClient, HandlerName, "system.notification.generated", payload)
			h.RedisClient.Set(ctx, cooldownKey, "1", 1*time.Hour)
		}
		return
	}

	log.Printf("[%s] Starting Fabricator Waterfall Run", HandlerName)

	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name, h.Config.ProcessID, h.DiscordClient)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	var runLogs strings.Builder
	runLogs.WriteString(fmt.Sprintf("Run started at %s\n\n", time.Now().Format(time.RFC3339)))

	// Capture initial commit hash to detect changes
	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")
	initialHash := ""
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		initialHash = strings.TrimSpace(string(out))
	}

	h.RedisClient.Set(ctx, "fabricator:active_tier", "review", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Review Protocol (Tier 0)")
	reviewOut, err := h.PerformReview(ctx)
	runLogs.WriteString(fmt.Sprintf("### TIER 0: REVIEW\nStatus: %v\nOutput: %s\n\n", err == nil, reviewOut))
	h.RedisClient.Set(ctx, "fabricator:last_run:review", time.Now().Unix(), 0)

	h.RedisClient.Set(ctx, "fabricator:active_tier", "issue", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Issue Protocol (Tier 1)")
	issueOut, err := h.PerformIssue(ctx)
	runLogs.WriteString(fmt.Sprintf("### TIER 1: ISSUE\nStatus: %v\nOutput: %s\n\n", err == nil, issueOut))
	h.RedisClient.Set(ctx, "fabricator:last_run:issue", time.Now().Unix(), 0)

	h.RedisClient.Set(ctx, "fabricator:active_tier", "construct", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Construct Protocol (Tier 2)")
	constructOut, err := h.PerformConstruct(ctx)
	runLogs.WriteString(fmt.Sprintf("### TIER 2: CONSTRUCT\nStatus: %v\nOutput: %s\n\n", err == nil, constructOut))
	h.RedisClient.Set(ctx, "fabricator:last_run:construct", time.Now().Unix(), 0)

	// Check for changes and trigger auto-build
	currentHash := ""
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		currentHash = strings.TrimSpace(string(out))
	}

	if initialHash != "" && currentHash != "" && initialHash != currentHash {
		log.Printf("[%s] Changes detected (%s -> %s). Triggering auto-build.", HandlerName, initialHash, currentHash)
		runLogs.WriteString("### AUTO-REBUILD\nChanges detected. System is re-building.\n\n")
		buildCmd := exec.Command("dex", "build", "--source", "--force")
		buildCmd.Dir = workingDir
		if out, err := buildCmd.CombinedOutput(); err != nil {
			log.Printf("ERROR: Auto-rebuild failed: %v\nOutput: %s", err, string(out))
			runLogs.WriteString(fmt.Sprintf("Build FAILED: %v\n", err))
		} else {
			runLogs.WriteString("Build SUCCESSFUL.\n")
		}
	}

	h.RedisClient.Set(ctx, "fabricator:active_tier", "reporter", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Reporter Protocol (Tier 3)")
	summary, _ := h.PerformReporter(ctx, runLogs.String())
	h.RedisClient.Set(ctx, "fabricator:last_run:reporter", time.Now().Unix(), 0)

	h.RedisClient.Del(ctx, "fabricator:active_tier")
	utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	if summary != "" && h.DiscordClient != nil {
		debugChannelID := "1426957003108122656"
		_, _ = h.DiscordClient.PostMessage(debugChannelID, "🛠️ **Fabricator Agent Run Summary**\n\n"+summary)
	}

	log.Printf("[%s] Fabricator Waterfall Run complete.", HandlerName)
}

func (h *FabricatorHandler) PerformReview(ctx context.Context) (string, error) {
	ids, err := h.RedisClient.ZRevRange(ctx, "events:timeline", 0, 49).Result()
	if err != nil {
		return "", err
	}

	var alerts strings.Builder
	for _, id := range ids {
		val, err := h.RedisClient.Get(ctx, "event:"+id).Result()
		if err != nil {
			continue
		}
		var e types.Event
		_ = json.Unmarshal([]byte(val), &e)
		var ed map[string]interface{}
		_ = json.Unmarshal(e.Event, &ed)
		eType, _ := ed["type"].(string)
		if eType == "system.notification.generated" || eType == "error_occurred" {
			alerts.WriteString(fmt.Sprintf("- [%s] %s: %s\n", e.ID, eType, string(e.Event)))
		}
	}

	if alerts.Len() == 0 {
		return "No alerts found.", nil
	}

	report, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["review"], alerts.String(), nil, nil)
	if err != nil {
		return "", err
	}

	if strings.Contains(report, "<NO_ISSUE/>") {
		return "Model reported NO ISSUE.", nil
	}

	repo := "EasterCompany/EasterCompany"
	title := "System Stability Issue (Automated Review)"
	body := report

	// Heuristic: Check if report specifies a repo
	if strings.Contains(report, "REPOSITORY:") {
		lines := strings.Split(report, "\n")
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "REPOSITORY:") {
				foundRepo := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "REPOSITORY:"))
				if foundRepo != "" {
					repo = foundRepo
					break
				}
			}
		}
	}

	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")

	cmd := exec.Command("gh", "issue", "create", "--repo", repo, "--title", title, "--body", body)
	cmd.Dir = workingDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}

	return "Issue created: " + string(out), nil
}

func (h *FabricatorHandler) PerformIssue(ctx context.Context) (string, error) {
	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")

	// 1. Get list of all repos (root + submodules)
	repos := []string{"EasterCompany/EasterCompany"}
	cmd := exec.Command("git", "submodule", "foreach", "--quiet", "git remote get-url origin")
	cmd.Dir = workingDir
	out, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, line := range lines {
			if strings.Contains(line, "github.com") {
				parts := strings.Split(strings.TrimSuffix(line, ".git"), "github.com/")
				if len(parts) > 1 {
					repos = append(repos, parts[1])
				}
			}
		}
	}

	// 2. Fetch oldest issue across all repos
	type IssueInfo struct {
		Repo   string
		Number int
		Title  string
		Body   string
	}
	var oldestIssue *IssueInfo

	for _, repo := range repos {
		cmd = exec.Command("gh", "issue", "list", "--repo", repo, "--state", "open", "--sort", "created", "--direction", "asc", "--limit", "1", "--json", "number,title,body")
		cmd.Dir = workingDir
		out, err = cmd.Output()
		if err != nil {
			continue
		}

		var issues []map[string]interface{}
		if err := json.Unmarshal(out, &issues); err == nil && len(issues) > 0 {
			issue := issues[0]
			num := int(issue["number"].(float64))
			if oldestIssue == nil {
				oldestIssue = &IssueInfo{Repo: repo, Number: num, Title: issue["title"].(string), Body: issue["body"].(string)}
			}
		}
	}

	if oldestIssue == nil {
		return "No open issues found.", nil
	}

	issueNum := fmt.Sprintf("%d", oldestIssue.Number)
	repo := oldestIssue.Repo

	comment := "Dexter has targeted this issue for autonomous investigation."
	cmd = exec.Command("gh", "issue", "comment", issueNum, "--repo", repo, "--body", comment)
	cmd.Dir = workingDir
	_ = cmd.Run()

	prompt := fmt.Sprintf("Issue #%s: %s\n\nBody: %s\n\nObjective: Investigate the codebase and create a detailed implementation plan. Do not modify any code.", issueNum, oldestIssue.Title, oldestIssue.Body)
	report, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["issue"], prompt, nil, nil)
	if err != nil {
		return "", err
	}

	cmd = exec.Command("gh", "issue", "comment", issueNum, "--repo", repo, "--body", "### Autonomous Investigation Report\n\n"+report)
	cmd.Dir = workingDir
	_ = cmd.Run()

	return fmt.Sprintf("Investigation complete for Issue #%s in %s", issueNum, repo), nil
}

func (h *FabricatorHandler) PerformConstruct(ctx context.Context) (string, error) {
	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")

	// 1. Get list of all repos (root + submodules)
	repos := []string{"EasterCompany/EasterCompany"}
	cmd := exec.Command("git", "submodule", "foreach", "--quiet", "git remote get-url origin")
	cmd.Dir = workingDir
	out, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, line := range lines {
			if strings.Contains(line, "github.com") {
				parts := strings.Split(strings.TrimSuffix(line, ".git"), "github.com/")
				if len(parts) > 1 {
					repos = append(repos, parts[1])
				}
			}
		}
	}

	// 2. Fetch oldest issue across all repos
	type IssueInfo struct {
		Repo   string
		Number int
		Title  string
		Body   string
	}
	var oldestIssue *IssueInfo

	for _, repo := range repos {
		cmd = exec.Command("gh", "issue", "list", "--repo", repo, "--state", "open", "--sort", "created", "--direction", "asc", "--limit", "1", "--json", "number,title,body")
		cmd.Dir = workingDir
		out, err = cmd.Output()
		if err != nil {
			continue
		}

		var issues []map[string]interface{}
		if err := json.Unmarshal(out, &issues); err == nil && len(issues) > 0 {
			issue := issues[0]
			num := int(issue["number"].(float64))
			if oldestIssue == nil {
				oldestIssue = &IssueInfo{Repo: repo, Number: num, Title: issue["title"].(string), Body: issue["body"].(string)}
			}
		}
	}

	if oldestIssue == nil {
		return "No open issues found.", nil
	}

	issueNum := fmt.Sprintf("%d", oldestIssue.Number)
	repo := oldestIssue.Repo

	prompt := fmt.Sprintf("IMPLEMENT FIX for Issue #%s: %s in %s\n\nObjective: Apply the necessary changes to the codebase. You are in YOLO mode. Verify with builds/tests. REQUIREMENTS: Changes must pass `dex fmt`, `dex lint`, and `dex build --source --force --dry-run` before completion.", issueNum, oldestIssue.Title, repo)
	result, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["construct"], prompt, nil, nil)
	if err != nil {
		return "", err
	}

	// Post-fabrication verification
	log.Printf("[%s] Verification phase starting for Issue #%s", HandlerName, issueNum)

	// Perform dry-run build to verify integrity
	buildCmd := exec.Command("dex", "build", "--source", "--force", "--dry-run")
	buildCmd.Dir = workingDir
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Sprintf("Construction FAILED verification: %v\nOutput: %s", err, string(out)), err
	}

	closingComment := "Dexter has implemented and verified the fix for this issue. Closing.\n\n### Implementation Summary\n" + result
	cmd = exec.Command("gh", "issue", "comment", issueNum, "--repo", repo, "--body", closingComment)
	cmd.Dir = workingDir
	_ = cmd.Run()

	cmd = exec.Command("gh", "issue", "close", issueNum, "--repo", repo)
	cmd.Dir = workingDir
	_ = cmd.Run()

	// Automatic Re-build and Deploy if changes detected
	log.Printf("[%s] Auto-rebuild phase starting...", HandlerName)
	rebuildCmd := exec.Command("dex", "build", "--source", "--force")
	rebuildCmd.Dir = workingDir
	if out, err := rebuildCmd.CombinedOutput(); err != nil {
		log.Printf("ERROR: Auto-rebuild failed: %v\nOutput: %s", err, string(out))
	} else {
		log.Printf("[%s] Auto-rebuild and deploy complete.", HandlerName)
	}

	return fmt.Sprintf("Issue #%s in %s closed and system re-built.", issueNum, repo), nil
}

func (h *FabricatorHandler) PerformReporter(ctx context.Context, logs string) (string, error) {
	summary, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["reporter"], logs, nil, nil)
	if err != nil {
		return "", err
	}
	return summary, nil
}

func (h *FabricatorHandler) ValidateLogic(res agent.AnalysisResult) []agent.Correction { return nil }
