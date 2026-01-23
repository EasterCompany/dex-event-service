package handlers

import (
	"context"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/EasterCompany/dex-event-service/internal/model"
	"github.com/EasterCompany/dex-event-service/internal/web"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/redis/go-redis/v9"
)

type Dependencies struct {
	Redis             *redis.Client
	Model             *model.Client
	Discord           *discord.Client
	Web               *web.Client
	Config            *config.ServiceMapConfig
	Options           *config.OptionsConfig
	EventServiceURL   string
	TTSServiceURL     string
	CheckInterruption func() bool
	// GetBackgroundHandler allows sync handlers to trigger logic in background workers
	GetBackgroundHandler func(name string) BackgroundHandler
}

type Handler interface {
	Handle(ctx context.Context, input types.HandlerInput, deps *Dependencies) (types.HandlerOutput, error)
}

type BackgroundHandler interface {
	Init(ctx context.Context) error
	Close() error
}

type HandlerFunc func(ctx context.Context, input types.HandlerInput, deps *Dependencies) (types.HandlerOutput, error)

func (f HandlerFunc) Handle(ctx context.Context, input types.HandlerInput, deps *Dependencies) (types.HandlerOutput, error) {
	return f(ctx, input, deps)
}
