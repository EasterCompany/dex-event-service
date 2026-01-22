package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

// RoadmapHandler routes roadmap-related requests.
func RoadmapHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/roadmap")
		path = strings.TrimPrefix(path, "/")

		if path == "stats" {
			GetRoadmapStatsHandler()(w, r)
			return
		}

		switch r.Method {
		case http.MethodGet:
			ListRoadmapHandler()(w, r)
		case http.MethodPost:
			if strings.HasSuffix(path, "/comment") {
				parts := strings.Split(path, "/")
				if len(parts) >= 2 {
					AddRoadmapCommentHandler(parts[0])(w, r)
					return
				}
			}
			CreateRoadmapItemHandler()(w, r)
		case http.MethodDelete:
			if path != "" {
				DeleteRoadmapItemHandler(path)(w, r)
			} else {
				http.Error(w, "Item ID required", http.StatusBadRequest)
			}
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// ListRoadmapHandler retrieves all roadmap items from GitHub.
func ListRoadmapHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		issues, err := utils.ListGitHubIssues()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to fetch issues: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}
}

// CreateRoadmapItemHandler creates a new GitHub issue.
func CreateRoadmapItemHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		number, err := utils.CreateGitHubIssue(req.Title, req.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to create issue: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"number": number, "status": "created"})
	}
}

// DeleteRoadmapItemHandler closes a GitHub issue.
func DeleteRoadmapItemHandler(id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		number, err := strconv.Atoi(id)
		if err != nil {
			http.Error(w, "Invalid issue number", http.StatusBadRequest)
			return
		}

		if err := utils.CloseGitHubIssue(number); err != nil {
			http.Error(w, fmt.Sprintf("Failed to close issue: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// AddRoadmapCommentHandler adds a comment to a GitHub issue.
func AddRoadmapCommentHandler(id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		number, err := strconv.Atoi(id)
		if err != nil {
			http.Error(w, "Invalid issue number", http.StatusBadRequest)
			return
		}

		var req struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if err := utils.AddGitHubIssueComment(number, req.Body); err != nil {
			http.Error(w, fmt.Sprintf("Failed to add comment: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("Comment added"))
	}
}

// GetRoadmapStatsHandler returns the count of open GitHub issues.
func GetRoadmapStatsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		count, err := utils.GetGitHubIssueCount()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to fetch issue count: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"open_issues": count})
	}
}
