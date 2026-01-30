package endpoints

import (
	"fmt"
	"net/http"
	"strings"
)

// FabricatorLiveHandler handles the SSE stream for fabricator live terminal output.
func FabricatorLiveHandler(w http.ResponseWriter, r *http.Request) {
	if RDB == nil {
		http.Error(w, "Redis not initialized", http.StatusInternalServerError)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// 1. Send buffer (last 100 chunks)
	buffer, err := RDB.LRange(r.Context(), "system:fabricator:live:buffer", 0, -1).Result()
	if err == nil && len(buffer) > 0 {
		for _, chunk := range buffer {
			// Format as SSE data
			// We need to escape newlines in data for SSE if they are within a single event,
			// but here each chunk is a separate event.
			_, _ = fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(chunk, "\n", "\\n"))
		}
		flusher.Flush()
	}

	// 2. Subscribe to live stream
	pubsub := RDB.Subscribe(r.Context(), "system:fabricator:live:stream")
	defer func() {
		_ = pubsub.Close()
	}()

	ch := pubsub.Channel()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(msg.Payload, "\n", "\\n"))
			flusher.Flush()
		}
	}
}
