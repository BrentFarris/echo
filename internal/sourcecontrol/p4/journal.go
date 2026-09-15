package p4

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/mutation"
	"github.com/brent/echo/internal/workspacefs"
)

type intent struct {
	Path                 string `json:"path"`
	Destination          string `json:"destination,omitempty"`
	Prior                record `json:"prior"`
	Change               string `json:"change"`
	Registered           bool   `json:"registered"`
	NativeMoved          bool   `json:"nativeMoved,omitempty"`
	MappingKnown         bool   `json:"mappingKnown,omitempty"`
	DepotPath            string `json:"depotPath,omitempty"`
	DestinationDepotPath string `json:"destinationDepotPath,omitempty"`
}
type operationRecord struct {
	ID           string             `json:"id"`
	Workspace    string             `json:"workspace"`
	Repository   string             `json:"repository"`
	Connection   connection         `json:"connection"`
	ClientRoot   string             `json:"clientRoot,omitempty"`
	Scopes       []workspacefs.Root `json:"scopes,omitempty"`
	RecoveryID   string             `json:"recoveryId,omitempty"`
	Kind         string             `json:"kind"`
	Origin       string             `json:"origin"`
	Files        []intent           `json:"files"`
	LocalApplied bool               `json:"localApplied"`
	Complete     bool               `json:"complete"`
	Diagnostic   string             `json:"diagnostic"`
	Created      time.Time          `json:"created"`
}

func (p *Provider) journalPath(r *operationRecord) string {
	return filepath.Join(p.dataDir, "journal", r.Repository, r.ID+".json")
}
func (p *Provider) persist(r *operationRecord) error {
	if r.Complete {
		if r.Kind == "delete" && r.RecoveryID != "" {
			if err := writeJSON(p.trashRecordPath(r.Workspace, r.RecoveryID), r); err != nil {
				return err
			}
		}
		err := os.Remove(p.journalPath(r))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return writeJSON(p.journalPath(r), r)
}

func (p *Provider) trashRecordPath(workspace, id string) string {
	return filepath.Join(p.dataDir, "trash", digest(workspace), digest(id)+".json")
}

// The Trash ID, not a path or timestamp, identifies the exact prior state.
func (p *Provider) recovery(workspace, id string) (*operationRecord, error) {
	if id == "" {
		return nil, nil
	}
	data, err := os.ReadFile(p.trashRecordPath(workspace, id))
	var saved operationRecord
	if err == nil {
		if json.Unmarshal(data, &saved) != nil || saved.Workspace != workspace || saved.RecoveryID != id {
			return nil, errors.New("the P4 Trash recovery record is damaged; review it before restoring: " + p.trashRecordPath(workspace, id))
		}
		return &saved, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("cannot read P4 Trash recovery record: %w", err)
	}
	directories, _ := os.ReadDir(filepath.Join(p.dataDir, "journal"))
	for _, directory := range directories {
		if !directory.IsDir() {
			continue
		}
		for _, record := range p.records(&repository{id: directory.Name(), workspace: workspace}) {
			if record.Kind == "delete" && record.RecoveryID == id && record.LocalApplied {
				return &record, nil
			}
		}
	}
	return nil, nil
}
func (p *Provider) records(r *repository) []operationRecord {
	base := filepath.Join(p.dataDir, "journal", r.id)
	entries, readErr := os.ReadDir(base)
	diagnostic := ""
	if readErr != nil && !os.IsNotExist(readErr) {
		diagnostic = "Cannot read the P4 recovery journal: " + readErr.Error()
	}
	defer func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.journalDiagnostics == nil {
			p.journalDiagnostics = map[string]string{}
		}
		p.journalDiagnostics[r.id] = diagnostic
	}()
	var records []operationRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, entry.Name()))
		if err != nil {
			diagnostic = "Cannot read a P4 recovery record: " + err.Error()
			continue
		}
		var record operationRecord
		if err := json.Unmarshal(data, &record); err != nil {
			diagnostic = "A P4 recovery record is damaged and needs review: " + filepath.Join(base, entry.Name())
		} else if record.Repository == r.id && record.Workspace == r.workspace {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Created.Before(records[j].Created) })
	return records
}
func (p *Provider) pending(r *repository) []operationRecord {
	var pending []operationRecord
	for _, record := range p.records(r) {
		if !record.Complete {
			if record.Diagnostic == "" {
				record.Diagnostic = "An interrupted Echo operation needs registration review; file contents will not be replayed"
			}
			pending = append(pending, record)
		}
	}
	return pending
}

func (p *Provider) retry(ctx context.Context, r *repository, paths []string) error {
	selected := map[string]bool{}
	for _, path := range paths {
		selected[path] = true
	}
	for _, record := range p.pending(r) {
		for i := range record.Files {
			file := &record.Files[i]
			if file.Registered || (!selected[file.Path] && !selected[file.Destination]) {
				continue
			}
			if !record.LocalApplied {
				return errors.New("this operation was interrupted before local completion was recorded; review reconciliation for its exact paths")
			}
			if record.Connection.Server != r.connection.Server || record.Connection.User != r.connection.User || record.Connection.Client != r.connection.Client {
				return errors.New("pending operation belongs to a different P4 connection")
			}
			if err := p.validateRecordedMapping(ctx, r, *file); err != nil {
				return err
			}
			if err := p.register(ctx, r, record.Kind, file, true); err != nil {
				record.Diagnostic = err.Error()
				_ = p.persist(&record)
				return err
			}
			file.Registered = true
		}
		record.Complete = true
		for _, file := range record.Files {
			if !file.Registered {
				record.Complete = false
			}
		}
		if err := p.persist(&record); err != nil {
			return err
		}
	}
	return nil
}

// A client name can stay the same while its View changes. Never redirect an
// interrupted operation to the new depot mapping without an explicit review.
func (p *Provider) validateRecordedMapping(ctx context.Context, r *repository, file intent) error {
	expected := file.DepotPath
	if expected == "" {
		expected = file.Prior["depotFile"]
	}
	if !file.MappingKnown && (expected == "" || file.Destination != "") {
		return errors.New("the original P4 mapping was unavailable; review reconciliation for these paths before registering them")
	}
	paths := map[string]string{file.Path: expected}
	if file.Destination != "" {
		paths[file.Destination] = file.DestinationDepotPath
	}
	for path, expected := range paths {
		if _, err := p.resolve(r, r.relative(path)); err != nil {
			return err
		}
		mapped, err := p.mapping(ctx, r, path)
		if err != nil && !errors.Is(err, errUnmapped) {
			return err
		}
		if mapped["depotFile"] != expected {
			return errors.New("the P4 client mapping changed since Echo's operation; review reconciliation before retrying")
		}
	}
	return nil
}

// Reconciliation is an explicit review of current disk and P4 state. Completing
// that review may retire old registration records; it never replays their data.
func (p *Provider) completeReviewed(r *repository, paths []string) error {
	selected := map[string]bool{}
	for _, path := range paths {
		selected[path] = true
	}
	for _, record := range p.pending(r) {
		for i := range record.Files {
			file := &record.Files[i]
			if selected[file.Path] && (file.Destination == "" || selected[file.Destination]) {
				file.Registered = true
			}
		}
		record.Complete = true
		for _, file := range record.Files {
			record.Complete = record.Complete && file.Registered
		}
		if err := p.persist(&record); err != nil {
			return err
		}
	}
	return nil
}

func pendingResult(record *operationRecord, err error) mutation.Result {
	record.Diagnostic = fmt.Sprintf("File operation completed; P4 registration needs review: %s", err)
	return mutation.Result{Pending: true, Diagnostic: record.Diagnostic, OperationID: record.ID}
}
