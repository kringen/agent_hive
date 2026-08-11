package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"agentplatform/router/internal/budget"
	"agentplatform/router/internal/classifier"
	"agentplatform/router/internal/health"
)

var (
	ollamaHost    = getEnv("OLLAMA_HOST", "http://kringai.local:11434")
	ollamaModel   = getEnv("OLLAMA_MODEL", "gemma2:9b")
	claudeModel   = getEnv("CLAUDE_MODEL", "claude-sonnet-4-6")
	natsURL       = getEnv("NATS_URL", "nats://nats.agent-platform.svc:4222")
	budgetDBPath  = getEnv("BUDGET_DB_PATH", "/data/router-budget.db")
	healthPort    = getEnv("HEALTH_PORT", "8080")
	monthlyBudget = getEnvFloat("MONTHLY_BUDGET_USD", 30.0)
)

// maxDeliverAttempts caps redelivery of a task before it's routed to the
// agent.tasks.dead-letter subject instead of retried forever.
const maxDeliverAttempts = 5

var claudeClient anthropic.Client

type Task struct {
	TaskID    string                 `json:"task_id"`
	TaskType  string                 `json:"task_type"`
	AgentName string                 `json:"agent_name"`
	Payload   map[string]interface{} `json:"payload"`
}

type Result struct {
	TaskID    string `json:"task_id"`
	Result    string `json:"result"`
	ModelUsed string `json:"model_used"`
	Degraded  bool   `json:"degraded"`
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
			return f
		}
	}
	return fallback
}

func callOllama(prompt string) (string, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model":  ollamaModel,
		"prompt": prompt,
		"stream": false,
	})

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Post(ollamaHost+"/api/generate", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("failed to close ollama response body: %v", cerr)
		}
	}()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var parsed struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	return parsed.Response, nil
}

// callClaude returns the response text plus input/output token counts for
// budget tracking.
func callClaude(prompt string) (text string, inputTokens, outputTokens int, err error) {
	msg, err := claudeClient.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.F(anthropic.Model(claudeModel)), //nolint:unconvert // explicit conversion documents intent even though Model is a string alias
		MaxTokens: anthropic.F(int64(1024)),
		Messages: anthropic.F([]anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		}),
	})
	if err != nil {
		return "", 0, 0, err
	}

	for _, block := range msg.Content {
		if block.Type == anthropic.ContentBlockTypeText {
			text += block.Text
		}
	}

	return text, int(msg.Usage.InputTokens), int(msg.Usage.OutputTokens), nil
}

func buildPrompt(taskType string, payload map[string]interface{}) string {
	data, _ := json.Marshal(payload)
	return fmt.Sprintf("Task: %s\nData: %s\n\nRespond concisely with only the requested output.", taskType, string(data))
}

func payloadText(payload map[string]interface{}) string {
	text, _ := payload["text"].(string)
	body, _ := payload["body"].(string)
	return text + body
}

func handleTask(task Task) Result {
	prompt := buildPrompt(task.TaskType, task.Payload)
	modelChoice := classifier.Classify(task.TaskType, payloadText(task.Payload))
	degraded := false
	var answer string

	if modelChoice == "ollama" {
		var err error
		answer, err = callOllama(prompt)
		if err != nil {
			log.Printf("ollama call failed: %v", err)
			answer = ""
		}

		if classifier.LooksLowConfidence(answer) {
			status, _ := budget.GetBudgetStatus(monthlyBudget)
			if !status.OverBudget {
				text, inTok, outTok, err := callClaude(prompt)
				if err == nil {
					answer = text
					if _, err := budget.RecordCall(claudeModel, inTok, outTok, task.TaskType); err != nil {
						log.Printf("failed to record claude call cost: %v", err)
					}
					modelChoice = "claude (escalated)"
				}
			} else {
				degraded = true
			}
		}
	} else {
		status, _ := budget.GetBudgetStatus(monthlyBudget)
		if status.OverBudget {
			var err error
			answer, err = callOllama(prompt)
			if err != nil {
				answer = ""
			}
			modelChoice = "ollama (budget fallback)"
			degraded = true
		} else {
			text, inTok, outTok, err := callClaude(prompt)
			if err != nil {
				log.Printf("claude call failed: %v", err)
				answer = ""
			} else {
				answer = text
				if _, err := budget.RecordCall(claudeModel, inTok, outTok, task.TaskType); err != nil {
					log.Printf("failed to record claude call cost: %v", err)
				}
			}
		}
	}

	return Result{TaskID: task.TaskID, Result: answer, ModelUsed: modelChoice, Degraded: degraded}
}

// handleTaskSafely wraps handleTask with panic recovery so a bug in one
// task's handling triggers a NATS redelivery instead of crashing the whole
// router process (which would fail every other in-flight task too).
func handleTaskSafely(task Task) (result Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic handling task %s: %v", task.TaskID, r)
		}
	}()
	result = handleTask(task)
	return result, nil
}

func main() {
	healthSrv := health.NewServer()
	healthSrv.AddCheck("budget_db", budget.Ping)

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", healthSrv.LivezHandler)
		mux.HandleFunc("GET /readyz", healthSrv.ReadyzHandler)
		srv := &http.Server{
			Addr:         ":" + healthPort,
			Handler:      mux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  30 * time.Second,
		}
		log.Printf("Health endpoints listening on :%s", healthPort)
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("health server exited: %v", err)
		}
	}()

	if err := budget.InitDB(budgetDBPath); err != nil {
		log.Fatalf("failed to init budget db: %v", err)
	}

	claudeClient = *anthropic.NewClient(option.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")))

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("failed to connect to nats: %v", err)
	}
	defer nc.Close()
	healthSrv.AddCheck("nats", func() error {
		if !nc.IsConnected() {
			return fmt.Errorf("nats connection not established")
		}
		return nil
	})

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("failed to init jetstream: %v", err)
	}

	// Creating an R3 stream requires the NATS cluster to have a quorum
	// available to elect a Raft leader for it. depends_on only waits for
	// containers to start, not for the cluster to actually be ready, so
	// retry with backoff instead of crash-looping on a transient timeout
	// during cluster startup.
	var stream jetstream.Stream
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		stream, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     "AGENT_TASKS",
			Subjects: []string{"agent.tasks.>"},
			// 3-way replication across the NATS cluster — the stream survives
			// the loss of any single node as long as a quorum (2/3) is up.
			Replicas: 3,
		})
		cancel()
		if err == nil {
			break
		}
		if attempt >= 10 {
			log.Fatalf("failed to create stream after %d attempts: %v", attempt, err)
		}
		log.Printf("stream creation attempt %d failed (%v), retrying...", attempt, err)
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}

	var consumer jetstream.Consumer
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		consumer, err = stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
			Durable: "router",
			// Explicit ack so a crashed/killed router redelivers in-flight
			// tasks instead of silently losing them.
			AckPolicy: jetstream.AckExplicitPolicy,
			AckWait:   30 * time.Second,
			// Give up after a few attempts and dead-letter rather than
			// retrying a poison message forever.
			MaxDeliver: maxDeliverAttempts,
			BackOff:    []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second},
		})
		cancel()
		if err == nil {
			break
		}
		if attempt >= 10 {
			log.Fatalf("failed to create consumer after %d attempts: %v", attempt, err)
		}
		log.Printf("consumer creation attempt %d failed (%v), retrying...", attempt, err)
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}

	healthSrv.MarkReady()
	log.Printf("Router online. Budget cap: $%.2f/mo", monthlyBudget)

	for {
		msgs, err := consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
		if err != nil {
			continue
		}
		for msg := range msgs.Messages() {
			var task Task
			if err := json.Unmarshal(msg.Data(), &task); err != nil {
				// Malformed message — no amount of redelivery will fix it.
				log.Printf("failed to unmarshal task, terminating message: %v", err)
				if err := msg.Term(); err != nil {
					log.Printf("failed to terminate message: %v", err)
				}
				continue
			}

			meta, metaErr := msg.Metadata()
			if metaErr == nil && meta.NumDelivered >= maxDeliverAttempts {
				log.Printf("task %s exceeded max delivery attempts, sending to dead-letter", task.TaskID)
				deadLetterBytes, _ := json.Marshal(task)
				if err := nc.Publish("agent.tasks.dead-letter", deadLetterBytes); err != nil {
					log.Printf("failed to publish dead-letter for task %s: %v", task.TaskID, err)
				}
				if err := msg.Term(); err != nil {
					log.Printf("failed to terminate message: %v", err)
				}
				continue
			}

			result, err := handleTaskSafely(task)
			if err != nil {
				log.Printf("task %s failed, will redeliver: %v", task.TaskID, err)
				if err := msg.Nak(); err != nil {
					log.Printf("failed to nak message: %v", err)
				}
				continue
			}

			resultBytes, _ := json.Marshal(result)
			if err := nc.Publish("agent.results."+task.TaskID, resultBytes); err != nil {
				log.Printf("failed to publish result for task %s: %v", task.TaskID, err)
			}

			status, _ := budget.GetBudgetStatus(monthlyBudget)
			statusBytes, _ := json.Marshal(status)
			if err := nc.Publish("agent.budget.updates", statusBytes); err != nil {
				log.Printf("failed to publish budget update: %v", err)
			}

			if err := msg.Ack(); err != nil {
				log.Printf("failed to ack message for task %s: %v", task.TaskID, err)
			}
		}
	}
}
