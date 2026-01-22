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
	log.Printf("[%s] Starting Fabricator Waterfall Run", HandlerName)

	utils.AcquireCognitiveLock(ctx, h.RedisClient, h.Config.Name, h.Config.ProcessID, h.DiscordClient)
	defer utils.ReleaseCognitiveLock(ctx, h.RedisClient, h.Config.Name)

	var runLogs strings.Builder
	runLogs.WriteString(fmt.Sprintf("Run started at %s\n\n", time.Now().Format(time.RFC3339)))

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

	cmd := exec.Command("gh", "issue", "list", "--repo", "EasterCompany/EasterCompany", "--state", "open", "--sort", "created", "--direction", "asc", "--limit", "1", "--json", "number,title,body")
	cmd.Dir = workingDir
	out, err := cmd.Output()
	if err != nil {
		return string(out), err
	}

	var issues []map[string]interface{}
	if err := json.Unmarshal(out, &issues); err != nil || len(issues) == 0 {
		return "No open issues found.", nil
	}

	issue := issues[0]
	issueNum := fmt.Sprintf("%v", issue["number"])
	issueTitle := fmt.Sprintf("%v", issue["title"])

	comment := "Dexter has targeted this issue for autonomous investigation."
	cmd = exec.Command("gh", "issue", "comment", issueNum, "--repo", "EasterCompany/EasterCompany", "--body", comment)
	cmd.Dir = workingDir
	_ = cmd.Run()

	prompt := fmt.Sprintf("Issue #%s: %s\n\nBody: %s\n\nObjective: Investigate the codebase and create a detailed implementation plan. Do not modify any code.", issueNum, issueTitle, issue["body"])
	report, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["issue"], prompt, nil, nil)
	if err != nil {
		return "", err
	}

	cmd = exec.Command("gh", "issue", "comment", issueNum, "--repo", "EasterCompany/EasterCompany", "--body", "### Autonomous Investigation Report\n\n"+report)
	cmd.Dir = workingDir
	_ = cmd.Run()

	return "Investigation complete for Issue #" + issueNum, nil
}

func (h *FabricatorHandler) PerformConstruct(ctx context.Context) (string, error) {
	homeDir, _ := os.UserHomeDir()
	workingDir := filepath.Join(homeDir, "EasterCompany")

	cmd := exec.Command("gh", "issue", "list", "--repo", "EasterCompany/EasterCompany", "--state", "open", "--sort", "created", "--direction", "asc", "--limit", "1", "--json", "number,title,body")
	cmd.Dir = workingDir
	out, err := cmd.Output()
	if err != nil {
		return string(out), err
	}

	var issues []map[string]interface{}
	if err := json.Unmarshal(out, &issues); err != nil || len(issues) == 0 {
		return "No open issues found.", nil
	}

	issue := issues[0]
	issueNum := fmt.Sprintf("%v", issue["number"])

	prompt := fmt.Sprintf("IMPLEMENT FIX for Issue #%s: %s\n\nObjective: Apply the necessary changes to the codebase. You are in YOLO mode. Verify with builds/tests.", issueNum, issue["title"])
	result, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["construct"], prompt, nil, nil)
	if err != nil {
		return "", err
	}

	closingComment := "Dexter has implemented and verified the fix for this issue. Closing.\n\n### Implementation Summary\n" + result
	cmd = exec.Command("gh", "issue", "comment", issueNum, "--repo", "EasterCompany/EasterCompany", "--body", closingComment)
	cmd.Dir = workingDir
	_ = cmd.Run()

	cmd = exec.Command("gh", "issue", "close", issueNum, "--repo", "EasterCompany/EasterCompany")
	cmd.Dir = workingDir
	_ = cmd.Run()

	return "Issue #" + issueNum + " closed.", nil
}

func (h *FabricatorHandler) PerformReporter(ctx context.Context, logs string) (string, error) {
	summary, _, err := h.ModelClient.GenerateWithContext(ctx, h.Config.Models["reporter"], logs, nil, nil)
	if err != nil {
		return "", err
	}
	return summary, nil
}

func (h *FabricatorHandler) ValidateLogic(res agent.AnalysisResult) []agent.Correction { return nil }
