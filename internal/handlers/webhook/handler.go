package webhook

import (
	"context"

	"github.com/EasterCompany/dex-event-service/internal/handlers"
	"github.com/EasterCompany/dex-event-service/types"
)

func Handle(ctx context.Context, input types.HandlerInput, deps *handlers.Dependencies) (types.HandlerOutput, error) {
	// log.Printf("Webhook handler received event: %s", input.EventID)

	return types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{},
	}, nil
}
