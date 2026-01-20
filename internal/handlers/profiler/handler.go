package profiler

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/handlers"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/redis/go-redis/v9"
)

// --- Shared Data Structure (Duplicated for decoupling) ---

type UserProfile struct {
	UserID         string         `json:"user_id"`
	Identity       Identity       `json:"identity"`
	CognitiveModel CognitiveModel `json:"cognitive_model"`
	Attributes     []Attribute    `json:"attributes"`
	Stats          UserStats      `json:"stats"`
	Dossier        Dossier        `json:"dossier"`
	Topics         []Topic        `json:"topics"`
	Traits         Psychometrics  `json:"traits"`
}

type Identity struct {
	Username  string   `json:"username"`
	AvatarURL string   `json:"avatar_url"`
	FirstSeen string   `json:"first_seen"`
	Badges    []string `json:"badges"`
	Status    string   `json:"status"`
}

type CognitiveModel struct {
	TechnicalLevel     float64 `json:"technical_level"`
	CommunicationStyle string  `json:"communication_style"`
	PatienceLevel      string  `json:"patience_level"`
	Vibe               string  `json:"vibe"`
}

type Attribute struct {
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

type UserStats struct {
	TotalMessages  int    `json:"total_messages"`
	LastActive     string `json:"last_active"`
	TokensConsumed int64  `json:"tokens_consumed"`
}

type Dossier struct {
	Identity IdentityDetails    `json:"identity"`
	Career   CareerDetails      `json:"career"`
	Personal PersonalDetails    `json:"personal"`
	Social   []SocialConnection `json:"social"`
}

type IdentityDetails struct {
	FullName     string `json:"fullName"`
	AgeRange     string `json:"ageRange"`
	Location     string `json:"location"`
	Gender       string `json:"gender"`
	Sexuality    string `json:"sexuality"`
	Relationship string `json:"relationship"`
}

type CareerDetails struct {
	JobTitle string   `json:"jobTitle"`
	Company  string   `json:"company"`
	Skills   []string `json:"skills"`
}

type PersonalDetails struct {
	Hobbies []string `json:"hobbies"`
	Habits  []string `json:"habits"`
	Vices   []string `json:"vices"`
	Virtues []string `json:"virtues"`
}

type SocialConnection struct {
	Name     string `json:"name"`
	Relation string `json:"relation"`
	Trust    string `json:"trust"`
}

type Topic struct {
	Name string `json:"name"`
	Val  int    `json:"val"`
}

type Psychometrics struct {
	Openness          int `json:"openness"`
	Conscientiousness int `json:"conscientiousness"`
	Extraversion      int `json:"extraversion"`
	Agreeableness     int `json:"agreeableness"`
	Neuroticism       int `json:"neuroticism"`
}

// --- Handler Logic ---

func Handle(ctx context.Context, input types.HandlerInput, deps *handlers.Dependencies) (types.HandlerOutput, error) {
	if deps.Redis == nil {
		return types.HandlerOutput{Success: false, Error: "Redis unavailable"}, nil
	}

	// --- 1. Universal User Indexing ---
	// Index every event that has a user_id or target_user_id into user-specific timelines
	userID, _ := input.EventData["user_id"].(string)
	targetUserID, _ := input.EventData["target_user_id"].(string)

	indexForUser := func(uid string) {
		if uid != "" && uid != "dexter" && uid != "dexter-bot" {
			userTimelineKey := "events:user:" + uid
			deps.Redis.ZAdd(ctx, userTimelineKey, redis.Z{
				Score:  float64(input.Timestamp),
				Member: input.EventID,
			})
			// Ensure the timeline key itself has a TTL
			deps.Redis.Expire(ctx, userTimelineKey, 24*time.Hour)
		}
	}

	indexForUser(userID)
	indexForUser(targetUserID)

	// --- 1.5 Index Mentions ---
	// Also index events into timelines of users who were mentioned
	if mentions, ok := input.EventData["mentions"].([]interface{}); ok {
		for _, m := range mentions {
			if mentionedUser, ok := m.(map[string]interface{}); ok {
				if mID, ok := mentionedUser["id"].(string); ok && mID != "" && mID != "dexter" {
					userTimelineKey := "events:user:" + mID
					deps.Redis.ZAdd(ctx, userTimelineKey, redis.Z{
						Score:  float64(input.Timestamp),
						Member: input.EventID,
					})
					deps.Redis.Expire(ctx, userTimelineKey, 24*time.Hour)
				}
			}
		}
	}

	eventType, _ := input.EventData["type"].(string)

	switch eventType {
	case "analysis.user.message_signals":
		return handleUserSignals(ctx, input, deps.Redis)
	case string(types.EventTypeMessagingBotSentMessage):
		return handleBotResponse(ctx, input, deps.Redis)
	}

	return types.HandlerOutput{Success: true}, nil
}

func handleUserSignals(ctx context.Context, input types.HandlerInput, r *redis.Client) (types.HandlerOutput, error) {
	userID, _ := input.EventData["user_id"].(string)
	if userID == "" {
		return types.HandlerOutput{Success: true}, nil
	}

	// 1. Load existing profile
	p, err := LoadProfile(ctx, r, userID)
	if err != nil {
		return types.HandlerOutput{Success: false, Error: err.Error()}, err
	}

	// 2. Extract signals
	signalsMap, ok := input.EventData["signals"].(map[string]interface{})
	if !ok {
		return types.HandlerOutput{Success: true}, nil
	}

	// 3. Update High-Frequency Data
	p.Stats.TotalMessages++
	p.Stats.LastActive = time.Now().Format(time.RFC3339)

	if mood, ok := signalsMap["mood"].(string); ok && mood != "" {
		p.CognitiveModel.Vibe = mood
	}

	// 4. Save
	if err := saveProfile(ctx, r, p); err != nil {
		return types.HandlerOutput{Success: false, Error: err.Error()}, err
	}

	log.Printf("Profiler: Updated metrics for user %s (Vibe: %s)", userID, p.CognitiveModel.Vibe)
	return types.HandlerOutput{Success: true}, nil
}

func handleBotResponse(ctx context.Context, input types.HandlerInput, r *redis.Client) (types.HandlerOutput, error) {
	// Bot responses represent "Cost" (Tokens). We should attribute these tokens to the user the bot was replying to.
	// We need to find the user ID from the parent event context if possible, or from the message context.
	// For now, let's look for a 'target_user_id' or similar if we have it.
	// Actually, bot sent message events have a 'channel_id'. We can't easily attribute tokens to a user without more context here
	// unless we pass the UserID through.

	// Wait, the botEventData I just updated includes 'eval_count' and 'prompt_count'.
	// I'll try to find the UserID from the parent message.

	// TODO: In a future iteration, we will ensure bot events carry the target UserID for cost attribution.
	return types.HandlerOutput{Success: true}, nil
}

func LoadProfile(ctx context.Context, r *redis.Client, userID string) (*UserProfile, error) {
	key := "user:profile:" + userID
	data, err := r.Get(ctx, key).Result()

	if err == redis.Nil {
		// New Profile
		return &UserProfile{
			UserID: userID,
			Identity: Identity{
				FirstSeen: time.Now().Format(time.RFC3339),
			},
		}, nil
	}
	if err != nil {
		return nil, err
	}

	var p UserProfile
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return nil, err
	}
	return &p, nil
}
func saveProfile(ctx context.Context, r *redis.Client, p *UserProfile) error {
	key := "user:profile:" + p.UserID
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return r.Set(ctx, key, data, 0).Err()
}
