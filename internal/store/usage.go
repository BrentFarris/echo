package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/brent/echo/internal/domain"
)

type usageLedger struct {
	domain.UsageTotals
	Baseline domain.UsageCounts `json:"baseline"`
}

func readUsage(tx *sql.Tx) (usageLedger, error) {
	var ledger usageLedger
	var data []byte
	err := tx.QueryRow("SELECT data FROM records WHERE namespace='usage' AND id='totals'").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		now := domain.Now()
		ledger.TrackingStartedAt, ledger.ResetAt = now, now
		err = writeUsageRecord(tx, "usage", "totals", ledger)
	} else if err == nil {
		err = json.Unmarshal(data, &ledger)
	}
	return ledger, err
}

func writeUsageRecord(tx *sql.Tx, namespace, id string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO records VALUES(?,?,?) ON CONFLICT(namespace,id) DO UPDATE SET data=excluded.data", namespace, id, data)
	return err
}

func (l usageLedger) current() domain.UsageTotals {
	value := l.UsageTotals
	value.InputTokens -= l.Baseline.InputTokens
	value.OutputTokens -= l.Baseline.OutputTokens
	value.ModelCalls -= l.Baseline.ModelCalls
	value.InputReports -= l.Baseline.InputReports
	value.OutputReports -= l.Baseline.OutputReports
	return value
}

func (s *Store) Usage() (domain.UsageTotals, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return domain.UsageTotals{}, err
	}
	defer tx.Rollback()
	ledger, err := readUsage(tx)
	if err != nil {
		return domain.UsageTotals{}, err
	}
	return ledger.current(), tx.Commit()
}

// RecordModelUsage is called only by the execution host, never by transcript
// persistence or replica merges. Deduplication and run statistics commit together.
func (s *Store) RecordModelUsage(requestID string, usage *domain.TokenUsage, runID string, statistics *domain.TurnStatistics) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT OR IGNORE INTO records VALUES('usage-requests',?,'true')", requestID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	ledger, err := readUsage(tx)
	if err != nil {
		return err
	}
	ledger.Add(usage)
	ledger.Revision++
	if err = writeUsageRecord(tx, "usage", "totals", ledger); err != nil {
		return err
	}
	if statistics != nil {
		if err = saveRunStatistics(tx, runID, statistics); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func saveRunStatistics(tx *sql.Tx, runID string, statistics *domain.TurnStatistics) error {
	data, err := json.Marshal(statistics)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE records SET data=json_set(data,'$.statistics',json(?)) WHERE namespace='runs' AND id=?", string(data), runID)
	return err
}

func (s *Store) SaveRunStatistics(runID string, statistics *domain.TurnStatistics) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = saveRunStatistics(tx, runID, statistics); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ResetUsage(requestID string) (domain.UsageTotals, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return domain.UsageTotals{}, err
	}
	defer tx.Rollback()
	var data []byte
	err = tx.QueryRow("SELECT data FROM records WHERE namespace='usage-resets' AND id=?", requestID).Scan(&data)
	if err == nil {
		var previous domain.UsageTotals
		err = json.Unmarshal(data, &previous)
		return previous, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.UsageTotals{}, err
	}
	ledger, err := readUsage(tx)
	if err != nil {
		return domain.UsageTotals{}, err
	}
	ledger.Baseline = ledger.UsageCounts
	ledger.ResetAt = domain.Now()
	ledger.Revision++
	if err = writeUsageRecord(tx, "usage", "totals", ledger); err != nil {
		return domain.UsageTotals{}, err
	}
	value := ledger.current()
	if err = writeUsageRecord(tx, "usage-resets", requestID, value); err != nil {
		return domain.UsageTotals{}, err
	}
	return value, tx.Commit()
}
