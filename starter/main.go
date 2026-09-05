package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

func main() {
	store := NewStore()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /events", handlePostEvents(store))
	mux.HandleFunc("GET /campaigns/{campaignID}/stats", handleGetStats(store))

	addr := ":8080"
	fmt.Printf("listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// handlePostEvents ingests a JSON array of events. A body that isn't valid
// JSON, or that is valid JSON but not an array, is rejected outright (400)
// with nothing processed. Once the array itself is confirmed, every
// element is validated and applied independently — one malformed element
// never prevents its siblings from being processed, and the response is
// always 200 for a successfully parsed array, with per-item outcomes in
// the body.
func handlePostEvents(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var raw []json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must be a JSON array of events"})
			return
		}
		if raw == nil {
			// Decode succeeds with a nil slice for a JSON `null` body;
			// null is not "a JSON array" per the contract.
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must be a JSON array of events"})
			return
		}

		resp := BatchResponse{Results: make([]ItemResult, 0, len(raw))}
		for i, item := range raw {
			var ev Event
			if err := json.Unmarshal(item, &ev); err != nil {
				resp.Results = append(resp.Results, ItemResult{
					Index:  i,
					Status: StatusRejected,
					Reason: "malformed event: " + err.Error(),
				})
				resp.Rejected++
				continue
			}

			res := store.ProcessEvent(ev)
			res.Index = i
			resp.Results = append(resp.Results, res)
			switch res.Status {
			case StatusAccepted:
				resp.Accepted++
			case StatusDuplicate:
				resp.Duplicate++
			case StatusConflict:
				resp.Conflict++
			case StatusRejected:
				resp.Rejected++
			}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// handleGetStats returns aggregated stats for one campaign.
func handleGetStats(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		campaignID := r.PathValue("campaignID")
		snap, ok := store.GetStats(campaignID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown campaign_id"})
			return
		}
		writeJSON(w, http.StatusOK, StatsResponse{
			CampaignID:  snap.CampaignID,
			Sent:        snap.Sent,
			Delivered:   snap.Delivered,
			Opened:      snap.Opened,
			UniqueOpens: snap.UniqueOpens,
			Clicked:     snap.Clicked,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}
