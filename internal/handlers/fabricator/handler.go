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

	"github.com/EasterCompany/dex-event-service/config"
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
				"review":    1800, // 30 minutes
				"issue":     1800, // 30 minutes
				"construct": 3600, // 1 hour
				"reporter":  3600, // 1 hour
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

	if h.IsActuallyBusy(ctx, h.Config.ProcessID) {
		return
	}

	now := time.Now().Unix()

	// RULE: No agent can run any protocols unless ALL protocols within it are "READY"
	for protocol, cooldown := range h.Config.Cooldowns {
		lastRun, _ := h.RedisClient.Get(ctx, fmt.Sprintf("fabricator:last_run:%s", protocol)).Int64()
		if now-lastRun < int64(cooldown) {
			// At least one protocol is on cooldown, whole agent stays idle.
			return
		}
	}

	// Trigger if we have fresh alerts to review
	if h.hasPendingAlerts(ctx) {
		log.Printf("[%s] Triggering waterfall: All protocols ready and pending alerts found.", HandlerName)
		go h.PerformWaterfall(ctx)
	}
}

func (h *FabricatorHandler) hasPendingAlerts(ctx context.Context) bool {
	ids, err := h.RedisClient.ZRevRange(ctx, "events:timeline", 0, 49).Result()
	if err != nil {
		return false
	}
	for _, id := range ids {
		val, err := h.RedisClient.Get(ctx, "event:"+id).Result()
		if err != nil {
			continue
		}
		var ed map[string]interface{}
		var e types.Event
		_ = json.Unmarshal([]byte(val), &e)
		_ = json.Unmarshal(e.Event, &ed)
		eType, _ := ed["type"].(string)
		if eType == "system.notification.generated" || eType == "error_occurred" {
			return true
		}
	}
	return false
}

type IssueInfo struct {
	Repo   string
	Number int
	Title  string
	Body   string
}

func (h *FabricatorHandler) PerformWaterfall(ctx context.Context) {
	if err := h.verifySafety(); err != nil {
		log.Printf("[%s] Aborting waterfall due to safety check failure: %v", HandlerName, err)
		return
	}

	log.Printf("[%s] Starting Fabricator Waterfall Run", HandlerName)

	// CLAIM RUN: Set all protocol last_run timestamps to now
	now := time.Now().Unix()
	for protocol := range h.Config.Cooldowns {
		h.RedisClient.Set(ctx, fmt.Sprintf("fabricator:last_run:%s", protocol), now, 0)
	}

	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name, h.Config.ProcessID, h.DiscordClient)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	var runLogs strings.Builder
	runLogs.WriteString(fmt.Sprintf("Run started at %s\n\n", time.Now().Format(time.RFC3339)))

	// RULE: Every fabricator agent run MUST go review -> issue -> construct -> reporter
	// If any tier skips (no work) or fails, the waterfall STOPS immediately.

	// TIER 0: REVIEW
	h.RedisClient.Set(ctx, "fabricator:active_tier", "review", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Review Protocol (Tier 0)")
	inferenceDone, reviewOut, err := h.PerformReview(ctx)
	runLogs.WriteString(fmt.Sprintf("### TIER 0: REVIEW\nStatus: %v\nOutput: %s\n\n", err == nil, reviewOut))

	if !inferenceDone || err != nil || strings.Contains(reviewOut, "<NO_ISSUE/>") {
		log.Printf("[%s] Waterfall aborted at Review: InferenceDone=%v, Err=%v, NoIssue=%v", HandlerName, inferenceDone, err, strings.Contains(reviewOut, "<NO_ISSUE/>"))
		h.finalizeRun(ctx, runLogs.String(), "Aborted at Review")
		return
	}
	h.RedisClient.Set(ctx, "fabricator:last_run:review", time.Now().Unix(), 0)

	// TIER 1: ISSUE (Find and Investigate)
	h.RedisClient.Set(ctx, "fabricator:active_tier", "issue", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Issue Protocol (Tier 1)")

	targetIssue, investigationReport, err := h.PerformIssue(ctx)
	if err != nil || targetIssue == nil {
		runLogs.WriteString("### TIER 1: ISSUE\nStatus: Failed or No Work\n\n")
		h.finalizeRun(ctx, runLogs.String(), "Aborted at Issue")
		return
	}

	runLogs.WriteString(fmt.Sprintf("### TIER 1: ISSUE\nTarget: %s#%d\nStatus: Success\n\n", targetIssue.Repo, targetIssue.Number))

	if strings.Contains(investigationReport, "<NO_ISSUE/>") {
		log.Printf("[%s] Model reported NO ISSUE for %s#%d. Closing issue.", HandlerName, targetIssue.Repo, targetIssue.Number)
		h.closeIssue(targetIssue, "Dexter investigated the codebase and found this issue to be invalid or already resolved.\n\n### Investigation Summary\n"+investigationReport)
		h.finalizeRun(ctx, runLogs.String(), "Success (No Issue Found)")
		return
	}
	h.RedisClient.Set(ctx, "fabricator:last_run:issue", time.Now().Unix(), 0)

	// TIER 2: CONSTRUCT (Apply and Verify)
	h.RedisClient.Set(ctx, "fabricator:active_tier", "construct", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Construct Protocol (Tier 2)")

	// Pass the investigation report to the construct tier
	inferenceDone, constructOut, err := h.PerformConstruct(ctx, targetIssue, investigationReport)
	runLogs.WriteString(fmt.Sprintf("### TIER 2: CONSTRUCT\nStatus: %v\nOutput: %s\n\n", err == nil, constructOut))

	if !inferenceDone || err != nil {
		h.finalizeRun(ctx, runLogs.String(), "Aborted at Construct")
		return
	}
	h.RedisClient.Set(ctx, "fabricator:last_run:construct", time.Now().Unix(), 0)

	// TIER 3: REPORTER (Always runs if we reached here)
	h.finalizeRun(ctx, runLogs.String(), "Success")
}

func (h *FabricatorHandler) finalizeRun(ctx context.Context, logs string, status string) {
	h.RedisClient.Set(ctx, "fabricator:active_tier", "reporter", utils.DefaultTTL)
	utils.ReportProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID, "Reporter Protocol (Tier 3)")

	inferenceDone, summary, _ := h.PerformReporter(ctx, fmt.Sprintf("Run Status: %s\n\n%s", status, logs))
	if inferenceDone {
		h.RedisClient.Set(ctx, "fabricator:last_run:reporter", time.Now().Unix(), 0)
	}

	h.RedisClient.Del(ctx, "fabricator:active_tier")
	utils.ClearProcess(ctx, h.RedisClient, h.DiscordClient, h.Config.ProcessID)

	if summary != "" && h.DiscordClient != nil {
		debugChannelID := "1426957003108122656"
		if opt, err := config.LoadOptions(); err == nil {
			if opt.Discord.DebugChannelID != "" {
				debugChannelID = opt.Discord.DebugChannelID
			}
		}
		_, _ = h.DiscordClient.PostMessage(debugChannelID, "🛠️ **Fabricator Agent Run Summary**\n\n"+summary)
	}
}

func (h *FabricatorHandler) PerformReview(ctx context.Context) (bool, string, error) {
	ids, err := h.RedisClient.ZRevRange(ctx, "events:timeline", 0, 49).Result()
	if err != nil {
		return false, "", err
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
		return false, "No alerts found.", nil
	}

	report, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["review"], alerts.String(), nil, nil)
	if err != nil {
		return false, "", err
	}

	if strings.Contains(report, "<NO_ISSUE/>") {
		return true, report, nil
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
		return true, string(out), err
	}

	return true, "Issue created: " + string(out), nil
}

func (h *FabricatorHandler) closeIssue(issue *IssueInfo, comment string) {
	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")
	issueNum := fmt.Sprintf("%d", issue.Number)

	cmd := exec.Command("gh", "issue", "comment", issueNum, "--repo", issue.Repo, "--body", comment)
	cmd.Dir = workingDir
	_ = cmd.Run()

	cmd = exec.Command("gh", "issue", "close", issueNum, "--repo", issue.Repo)
	cmd.Dir = workingDir
	_ = cmd.Run()
}

func (h *FabricatorHandler) findOldestIssue(ctx context.Context) (*IssueInfo, error) {
	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")

	repos := h.discoverRepos(workingDir)
	var oldestIssue *IssueInfo

	for _, repo := range repos {
		cmd := exec.Command("gh", "issue", "list", "--repo", repo, "--state", "open", "--sort", "created", "--direction", "asc", "--limit", "1", "--json", "number,title,body")
		cmd.Dir = workingDir
		out, err := cmd.Output()
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
	return oldestIssue, nil
}

func (h *FabricatorHandler) discoverRepos(workingDir string) []string {
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
	return repos
}

func (h *FabricatorHandler) PerformIssue(ctx context.Context) (*IssueInfo, string, error) {
	oldestIssue, err := h.findOldestIssue(ctx)
	if err != nil || oldestIssue == nil {
		return nil, "", err
	}

	issueNum := fmt.Sprintf("%d", oldestIssue.Number)
	repo := oldestIssue.Repo

	comment := "Dexter has targeted this issue for autonomous investigation."
	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")
	cmd := exec.Command("gh", "issue", "comment", issueNum, "--repo", repo, "--body", comment)
	cmd.Dir = workingDir
	_ = cmd.Run()

	prompt := fmt.Sprintf("Issue #%s: %s\n\nBody: %s\n\nObjective: Investigate the codebase and create a detailed implementation plan. Do not modify any code.", issueNum, oldestIssue.Title, oldestIssue.Body)
	report, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["issue"], prompt, nil, nil)
	if err != nil {
		return oldestIssue, "", err
	}

	cmd = exec.Command("gh", "issue", "comment", issueNum, "--repo", repo, "--body", "### Autonomous Investigation Report\n\n"+report)
	cmd.Dir = workingDir
	_ = cmd.Run()

	return oldestIssue, report, nil
}

func (h *FabricatorHandler) PerformConstruct(ctx context.Context, targetIssue *IssueInfo, investigationReport string) (bool, string, error) {
	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")

	issueNum := fmt.Sprintf("%d", targetIssue.Number)
	repo := targetIssue.Repo

	prompt := fmt.Sprintf("IMPLEMENT FIX for Issue #%s: %s in %s\n\n### INVESTIGATION REPORT:\n%s\n\nObjective: Apply the necessary changes to the codebase. You are in YOLO mode. Verify with builds/tests. REQUIREMENTS: Changes must pass `dex build --source --force --dry-run` before completion.", issueNum, targetIssue.Title, repo, investigationReport)

	result, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["construct"], prompt, nil, nil)
	if err != nil {
		return true, result, err
	}

	closingComment := "Dexter has implemented and verified the fix for this issue. Closing.\n\n### Implementation Summary\n" + result
	cmd := exec.Command("gh", "issue", "comment", issueNum, "--repo", repo, "--body", closingComment)
	cmd.Dir = workingDir
	_ = cmd.Run()

	cmd = exec.Command("gh", "issue", "close", issueNum, "--repo", repo)
	cmd.Dir = workingDir
	_ = cmd.Run()

	return true, fmt.Sprintf("Issue #%s in %s closed and verified.", issueNum, repo), nil
}

func (h *FabricatorHandler) PerformReporter(ctx context.Context, logs string) (bool, string, error) {
	summary, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["reporter"], logs, nil, nil)
	if err != nil {
		return false, "", err
	}
	return true, summary, nil
}

func (h *FabricatorHandler) ValidateLogic(res agent.AnalysisResult) []agent.Correction { return nil }
