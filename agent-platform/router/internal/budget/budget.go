package budget

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// Pricing: rough $ per 1M tokens. Update if pricing changes — intentionally
// simple, not a billing-grade calculator.
var pricing = map[string]struct{ Input, Output float64 }{
	"claude-sonnet-4-6":         {Input: 3.00, Output: 15.00},
	"claude-haiku-4-5-20251001": {Input: 0.80, Output: 4.00},
}

type BudgetStatus struct {
	MonthSpend float64 `json:"month_spend"`
	MonthlyCap float64 `json:"monthly_cap"`
	Remaining  float64 `json:"remaining"`
	OverBudget bool    `json:"over_budget"`
}

var db *sql.DB

// InitDB opens (creating if necessary) the SQLite budget database at path
// and ensures the schema exists.
func InitDB(path string) error {
	var err error
	db, err = sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS calls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts REAL NOT NULL,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			cost_usd REAL NOT NULL,
			task_type TEXT
		)
	`)
	return err
}

// Ping verifies the budget database connection is alive. Intended for use
// in readiness checks.
func Ping() error {
	return db.Ping()
}

// RecordCall persists a single model call's token usage and computed cost.
func RecordCall(model string, inputTokens, outputTokens int, taskType string) (float64, error) {
	p, ok := pricing[model]
	if !ok {
		p = struct{ Input, Output float64 }{Input: 3.00, Output: 15.00}
	}
	cost := (float64(inputTokens)/1_000_000)*p.Input + (float64(outputTokens)/1_000_000)*p.Output

	_, err := db.Exec(
		"INSERT INTO calls (ts, model, input_tokens, output_tokens, cost_usd, task_type) VALUES (?, ?, ?, ?, ?, ?)",
		float64(time.Now().Unix()), model, inputTokens, outputTokens, cost, taskType,
	)
	return cost, err
}

// GetBudgetStatus returns current month-to-date spend against monthlyCap.
func GetBudgetStatus(monthlyCap float64) (BudgetStatus, error) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()

	var spend float64
	err := db.QueryRow("SELECT COALESCE(SUM(cost_usd), 0) FROM calls WHERE ts >= ?", float64(monthStart)).Scan(&spend)
	if err != nil {
		return BudgetStatus{}, err
	}

	return BudgetStatus{
		MonthSpend: spend,
		MonthlyCap: monthlyCap,
		Remaining:  monthlyCap - spend,
		OverBudget: spend >= monthlyCap,
	}, nil
}
