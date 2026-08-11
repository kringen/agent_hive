// Package approvals implements the human-in-the-loop approval queue: a
// SQLite-backed store of actions proposed by agents, plus a WebSocket hub
// that relays live NATS task/result/budget events to the dashboard UI.
package approvals

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
	_ "modernc.org/sqlite"
)

var db *sql.DB

// ---------------------------------------------------------------- storage

type ProposedAction struct {
	ID         int64                  `json:"id"`
	AgentName  string                 `json:"agent_name"`
	ActionType string                 `json:"action_type"`
	TargetRef  string                 `json:"target_ref"`
	Payload    map[string]interface{} `json:"payload"`
	Status     string                 `json:"status"`
	CreatedAt  float64                `json:"created_at"`
}

// InitDB opens (creating if necessary) the SQLite approvals database at
// path and ensures the schema exists.
func InitDB(path string) error {
	var err error
	db, err = sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS proposed_actions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_name TEXT NOT NULL,
			action_type TEXT NOT NULL,
			target_ref TEXT NOT NULL,
			payload TEXT NOT NULL,
			status TEXT DEFAULT 'pending',
			created_at REAL NOT NULL,
			resolved_at REAL
		)
	`)
	return err
}

// Ping verifies the approvals database connection is alive. Intended for
// use in readiness checks.
func Ping() error {
	return db.Ping()
}

// ------------------------------------------------------------ websocket hub

type Hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]bool
}

func NewHub() *Hub {
	return &Hub{clients: make(map[*websocket.Conn]bool)}
}

func (h *Hub) add(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = true
}

func (h *Hub) remove(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
	if err := c.Close(); err != nil {
		log.Printf("failed to close websocket connection: %v", err)
	}
}

func (h *Hub) broadcast(event map[string]interface{}) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			if cerr := c.Close(); cerr != nil {
				log.Printf("failed to close websocket connection: %v", cerr)
			}
			delete(h.clients, c)
		}
	}
}

var hub = NewHub()
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // home-lab tool; tighten if exposed beyond your network
}

func WSHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	hub.add(conn)
	defer hub.remove(conn)

	// Not expecting client -> server messages; block here until disconnect.
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

// ------------------------------------------------------------- HTTP routes

func CreateProposedActionHandler(w http.ResponseWriter, r *http.Request) {
	var action ProposedAction
	if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	payloadJSON, _ := json.Marshal(action.Payload)
	now := float64(time.Now().Unix())

	res, err := db.Exec(
		"INSERT INTO proposed_actions (agent_name, action_type, target_ref, payload, created_at) VALUES (?, ?, ?, ?, ?)",
		action.AgentName, action.ActionType, action.TargetRef, string(payloadJSON), now,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, _ := res.LastInsertId()
	action.ID = id
	action.Status = "pending"

	hub.broadcast(map[string]interface{}{
		"type": "approval_created", "id": id, "agent_name": action.AgentName,
		"action_type": action.ActionType, "target_ref": action.TargetRef, "payload": action.Payload,
	})

	if err := json.NewEncoder(w).Encode(action); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}

func ListProposedActionsHandler(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}

	rows, err := db.Query(
		"SELECT id, agent_name, action_type, target_ref, payload, status, created_at FROM proposed_actions WHERE status = ? ORDER BY created_at DESC",
		status,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Printf("failed to close rows: %v", err)
		}
	}()

	var results []ProposedAction
	for rows.Next() {
		var a ProposedAction
		var payloadRaw string
		if err := rows.Scan(&a.ID, &a.AgentName, &a.ActionType, &a.TargetRef, &payloadRaw, &a.Status, &a.CreatedAt); err != nil {
			continue
		}
		if err := json.Unmarshal([]byte(payloadRaw), &a.Payload); err != nil {
			log.Printf("failed to unmarshal payload for action %d: %v", a.ID, err)
		}
		results = append(results, a)
	}

	if err := json.NewEncoder(w).Encode(results); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}

// ResolveActionHandler handles POST /proposed-actions/{id}/{decision}.
// NOTE: this only records the decision and notifies the UI — actually
// *executing* an approved action (e.g. applying a Gmail label) stays in
// the owning agent, which checks its own approved items on its next run.
func ResolveActionHandler(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	decision := r.PathValue("decision")

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if decision != "approve" && decision != "reject" {
		http.Error(w, "decision must be 'approve' or 'reject'", http.StatusBadRequest)
		return
	}

	status := "approved"
	if decision == "reject" {
		status = "rejected"
	}

	_, err = db.Exec("UPDATE proposed_actions SET status = ?, resolved_at = ? WHERE id = ?", status, float64(time.Now().Unix()), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	hub.broadcast(map[string]interface{}{"type": "approval_resolved", "id": id, "status": status})
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"id": id, "status": status}); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}

// ---------------------------------------------------------------- NATS relay

var natsConn *nats.Conn

// IsNatsConnected reports whether the NATS relay connection is currently
// up. Intended for use in readiness checks.
func IsNatsConnected() error {
	if natsConn == nil || !natsConn.IsConnected() {
		return fmt.Errorf("nats connection not established")
	}
	return nil
}

// StartNatsRelay subscribes to agent task/result/budget subjects and relays
// each event to connected WebSocket clients. Blocks establishing the
// connection but returns immediately after subscribing; callers typically
// run it in a goroutine.
func StartNatsRelay(natsURL string) error {
	nc, err := nats.Connect(natsURL)
	if err != nil {
		return err
	}
	natsConn = nc

	if _, err := nc.Subscribe("agent.tasks.>", func(msg *nats.Msg) {
		var event map[string]interface{}
		if json.Unmarshal(msg.Data, &event) == nil {
			event["type"] = "task_received"
			hub.broadcast(event)
		}
	}); err != nil {
		return fmt.Errorf("failed to subscribe to agent.tasks.>: %w", err)
	}

	if _, err := nc.Subscribe("agent.results.>", func(msg *nats.Msg) {
		var event map[string]interface{}
		if json.Unmarshal(msg.Data, &event) == nil {
			event["type"] = "task_completed"
			hub.broadcast(event)
		}
	}); err != nil {
		return fmt.Errorf("failed to subscribe to agent.results.>: %w", err)
	}

	if _, err := nc.Subscribe("agent.budget.updates", func(msg *nats.Msg) {
		var event map[string]interface{}
		if json.Unmarshal(msg.Data, &event) == nil {
			event["type"] = "budget_update"
			hub.broadcast(event)
		}
	}); err != nil {
		return fmt.Errorf("failed to subscribe to agent.budget.updates: %w", err)
	}

	log.Println("Subscribed to NATS agent events")
	return nil
}

// CorsMiddleware allows the static frontend to call this API directly
// during local dev; the nginx proxy config makes this unnecessary in the
// docker-compose/K8s setup but it's harmless to leave on for a home tool.
func CorsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			return
		}
		next.ServeHTTP(w, r)
	})
}
