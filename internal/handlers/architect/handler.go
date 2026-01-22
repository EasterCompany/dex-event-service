package architect

import (
	"context"
	"log"

	"github.com/EasterCompany/dex-event-service/internal/agent"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/EasterCompany/dex-event-service/internal/model"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

const (
	HandlerName        = "architect-handler"
	ArchitectProcessID = "system-architect"
)

type ArchitectHandler struct {
	agent.BaseAgent
	Config        agent.AgentConfig
	DiscordClient *discord.Client
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
}

func NewArchitectHandler(redis *redis.Client, modelClient *model.Client, discord *discord.Client) *ArchitectHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &ArchitectHandler{
		BaseAgent: agent.BaseAgent{
			RedisClient: redis,
			ModelClient: modelClient,
			ChatManager: utils.NewChatContextManager(redis),
		},
		Config: agent.AgentConfig{
			Name:            "Architect",
			ProcessID:       ArchitectProcessID,
			Models:          map[string]string{},
			ProtocolAliases: map[string]string{},
			Cooldowns:       map[string]int{},
			IdleRequirement: 300,
		},
		DiscordClient: discord,
		ctx:           ctx,
		cancel:        cancel,
	}
}

func (h *ArchitectHandler) Init(ctx context.Context) error {
	h.stopChan = make(chan struct{})
	log.Printf("[%s] Background worker started.", HandlerName)
	return nil
}

func (h *ArchitectHandler) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.stopChan != nil {
		close(h.stopChan)
	}
	utils.ClearProcess(context.Background(), h.RedisClient, h.DiscordClient, h.Config.ProcessID)
	return nil
}

func (h *ArchitectHandler) GetConfig() agent.AgentConfig {
	return h.Config
}

func (h *ArchitectHandler) Run(ctx context.Context) ([]agent.AnalysisResult, string, error) {
	return nil, "", nil
}

func (h *ArchitectHandler) ValidateLogic(res agent.AnalysisResult) []agent.Correction {
	return nil
}
