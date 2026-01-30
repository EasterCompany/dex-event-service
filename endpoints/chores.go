package endpoints

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/EasterCompany/dex-event-service/internal/chores"
	"github.com/redis/go-redis/v9"
)

// ChoresHandler routes requests for the chores API
func ChoresHandler(RDB *redis.Client) http.HandlerFunc {
	store := chores.NewStore(RDB)

	return func(w http.ResponseWriter, r *http.Request) {
		// Extract path after /chores
		path := strings.TrimPrefix(r.URL.Path, "/chores")
		path = strings.TrimPrefix(path, "/")

		// /chores -> List or Create
		if path == "" {
			switch r.Method {
			case http.MethodGet:
				handleListChores(w, r, store)
			case http.MethodPost:
				handleCreateChore(w, r, store)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}

		// /chores/{id} or /chores/{id}/run
		parts := strings.Split(path, "/")
		id := parts[0]

		if len(parts) == 1 {
			// /chores/{id}
			switch r.Method {
			case http.MethodGet:
				handleGetChore(w, r, store, id)
			case http.MethodPatch:
				handleUpdateChore(w, r, store, id)
			case http.MethodDelete:
				handleDeleteChore(w, r, store, id)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
			return
		} else if len(parts) == 2 && parts[1] == "run" && r.Method == http.MethodPost {
			// /chores/{id}/run
			handleMarkChoreRun(w, r, store, id)
			return
		} else {
			http.NotFound(w, r)
		}
	}
}

func handleListChores(w http.ResponseWriter, r *http.Request, store *chores.Store) {
	ctx := r.Context()
	list, err := store.GetAll(ctx)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func handleCreateChore(w http.ResponseWriter, r *http.Request, store *chores.Store) {
	var req chores.CreateChoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Recipients) == 0 || req.NaturalInstruction == "" {
		http.Error(w, "recipients and natural_instruction are required", http.StatusBadRequest)
		return
	}

	chore, err := store.Create(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(chore)
}

func handleGetChore(w http.ResponseWriter, r *http.Request, store *chores.Store, id string) {
	chore, err := store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chore)
}

func handleUpdateChore(w http.ResponseWriter, r *http.Request, store *chores.Store, id string) {
	var req chores.UpdateChoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	chore, err := store.Update(r.Context(), id, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chore)
}

func handleDeleteChore(w http.ResponseWriter, r *http.Request, store *chores.Store, id string) {
	if err := store.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleMarkChoreRun(w http.ResponseWriter, r *http.Request, store *chores.Store, id string) {
	var req struct {
		Memory []string `json:"memory"`
	}
	// Decode is optional, memory might be nil/empty
	_ = json.NewDecoder(r.Body).Decode(&req)

	if err := store.MarkRun(r.Context(), id, req.Memory); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
