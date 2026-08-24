// Package trajectory persists the append-only audit stream that backs Echo's
// Trajectory view. The transcript remains the source of truth for resuming a
// chat; this log is deliberately independent so inspection never changes the
// conversation.
package trajectory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const FormatVersion = 1

type Header struct {
	Record        string    `json:"record"`
	FormatVersion int       `json:"formatVersion"`
	ChatID        string    `json:"chatId"`
	Surface       string    `json:"surface"`
	CreatedAt     time.Time `json:"createdAt"`
}

type Event struct {
	Record    string          `json:"record"`
	Sequence  uint64          `json:"sequence"`
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	TurnID    string          `json:"turnId,omitempty"`
	Step      *int            `json:"step,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// AppendEntry describes one event to append. Timestamp may be supplied when
// events are buffered before persistence; a zero value uses the append time.
type AppendEntry struct {
	Timestamp time.Time
	Type      string
	TurnID    string
	Step      *int
	Data      any
}

type Page struct {
	Header     Header  `json:"header"`
	Events     []Event `json:"events"`
	Incomplete bool    `json:"incomplete,omitempty"`
	Warning    string  `json:"warning,omitempty"`
	HasMore    bool    `json:"hasMore"`
	OldestSeq  uint64  `json:"oldestSeq,omitempty"`
	NewestSeq  uint64  `json:"newestSeq,omitempty"`
}

type SearchResult struct {
	Header     Header  `json:"header"`
	Events     []Event `json:"events"`
	Incomplete bool    `json:"incomplete,omitempty"`
	Warning    string  `json:"warning,omitempty"`
	HasMore    bool    `json:"hasMore"`
}

// Store is safe for concurrent append and read operations. It owns no long-
// lived file descriptor, which keeps clear/close deletion reliable on Windows.
type Store struct {
	mu          sync.Mutex
	path        string
	chatID      string
	surface     string
	initialized bool
	header      Header
	nextSeq     uint64
	incomplete  bool
}

func New(workspacePath, chatID, surface string) (*Store, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" || chatID == "." || chatID == ".." || strings.ContainsAny(chatID, `/\\`) {
		return nil, fmt.Errorf("invalid trajectory chat id %q", chatID)
	}
	return &Store{
		path:    filepath.Join(workspacePath, ".echo", "trajectories", chatID+".jsonl"),
		chatID:  chatID,
		surface: strings.TrimSpace(surface),
	}, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Exists() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := os.Stat(s.path)
	return err == nil && info.Size() > 0
}

func (s *Store) Append(eventType, turnID string, step *int, data any) (Event, error) {
	events, err := s.AppendBatch([]AppendEntry{{Type: eventType, TurnID: turnID, Step: step, Data: data}})
	if err != nil {
		return Event{}, err
	}
	return events[0], nil
}

// AppendBatch persists multiple JSONL events with one open/write/close cycle.
// Every event remains a separate line with its own sequence and timestamp.
func (s *Store) AppendBatch(entries []AppendEntry) ([]Event, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.initializeLocked(); err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(entries))
	for index, entry := range entries {
		payload, err := json.Marshal(entry.Data)
		if err != nil {
			return nil, fmt.Errorf("marshal trajectory event %d: %w", index+1, err)
		}
		eventType := strings.TrimSpace(entry.Type)
		if eventType == "" {
			return nil, errors.New("trajectory event type is required")
		}
		timestamp := entry.Timestamp
		if timestamp.IsZero() {
			timestamp = time.Now()
		}
		var step *int
		if entry.Step != nil {
			value := *entry.Step
			step = &value
		}
		events = append(events, Event{
			Record: "event", Sequence: s.nextSeq + uint64(index), Timestamp: timestamp.UTC(),
			Type: eventType, TurnID: strings.TrimSpace(entry.TurnID), Step: step, Data: payload,
		})
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return nil, fmt.Errorf("create trajectory directory: %w", err)
	}
	if s.incomplete {
		if err := s.repairTrailingRecordLocked(); err != nil {
			return nil, err
		}
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open trajectory log: %w", err)
	}
	var output bytes.Buffer
	if info, statErr := file.Stat(); statErr != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat trajectory log: %w", statErr)
	} else if info.Size() == 0 {
		if err := writeLine(&output, s.header); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	for _, event := range events {
		if err := writeLine(&output, event); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	written, writeErr := file.Write(output.Bytes())
	if writeErr != nil || written != output.Len() {
		_ = file.Close()
		s.initialized = false
		if writeErr != nil {
			return nil, fmt.Errorf("write trajectory log: %w", writeErr)
		}
		return nil, fmt.Errorf("write trajectory log: %w", io.ErrShortWrite)
	}
	if err := file.Close(); err != nil {
		s.initialized = false
		return nil, fmt.Errorf("close trajectory log: %w", err)
	}
	s.nextSeq += uint64(len(events))
	return events, nil
}

func (s *Store) repairTrailingRecordLocked() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.incomplete = false
		return nil
	}
	if err != nil {
		return fmt.Errorf("read incomplete trajectory log: %w", err)
	}
	lastNewline := bytes.LastIndexByte(data, '\n')
	truncateAt := int64(0)
	if lastNewline >= 0 {
		truncateAt = int64(lastNewline + 1)
	}
	if err := os.Truncate(s.path, truncateAt); err != nil {
		return fmt.Errorf("repair incomplete trajectory log: %w", err)
	}
	s.incomplete = false
	return nil
}

func (s *Store) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete trajectory log: %w", err)
	}
	s.initialized = false
	s.header = Header{}
	s.nextSeq = 1
	s.incomplete = false
	return nil
}

func (s *Store) Page(beforeSeq uint64, turnLimit int) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	header, events, incomplete, err := s.readLocked()
	if err != nil {
		return Page{}, err
	}
	if turnLimit <= 0 {
		turnLimit = 20
	}
	if turnLimit > 100 {
		turnLimit = 100
	}
	filtered := make([]Event, 0, len(events))
	for _, event := range events {
		if beforeSeq == 0 || event.Sequence < beforeSeq {
			filtered = append(filtered, event)
		}
	}
	start := pageStart(filtered, turnLimit)
	pageEvents := append([]Event(nil), filtered[start:]...)
	page := Page{Header: header, Events: pageEvents, Incomplete: incomplete, HasMore: start > 0}
	if len(pageEvents) > 0 {
		page.OldestSeq = pageEvents[0].Sequence
		page.NewestSeq = pageEvents[len(pageEvents)-1].Sequence
	}
	return page, nil
}

func (s *Store) Search(query string, beforeSeq uint64, limit int) (SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	header, events, incomplete, err := s.readLocked()
	if err != nil {
		return SearchResult{}, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	matches := make([]Event, 0, limit)
	hasMore := false
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if beforeSeq != 0 && event.Sequence >= beforeSeq {
			continue
		}
		encoded, _ := json.Marshal(event)
		if query != "" && !bytes.Contains(bytes.ToLower(encoded), []byte(query)) {
			continue
		}
		if len(matches) == limit {
			hasMore = true
			break
		}
		matches = append(matches, event)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Sequence < matches[j].Sequence })
	return SearchResult{Header: header, Events: matches, Incomplete: incomplete, HasMore: hasMore}, nil
}

func (s *Store) Open() (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.initializeLocked(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return nil, fmt.Errorf("create trajectory directory: %w", err)
	}
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if createErr != nil {
			return nil, fmt.Errorf("create trajectory export: %w", createErr)
		}
		if writeErr := writeLine(file, s.header); writeErr != nil {
			_ = file.Close()
			return nil, writeErr
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("close trajectory export: %w", closeErr)
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat trajectory export: %w", err)
	}
	file, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open trajectory export: %w", err)
	}
	return file, nil
}

func (s *Store) initializeLocked() error {
	if s.initialized {
		return nil
	}
	header, events, incomplete, err := s.readFileLocked()
	if err != nil {
		return err
	}
	if header.Record == "" {
		header = Header{
			Record: "header", FormatVersion: FormatVersion, ChatID: s.chatID,
			Surface: s.surface, CreatedAt: time.Now().UTC(),
		}
	}
	s.header = header
	s.nextSeq = 1
	for _, event := range events {
		if event.Sequence >= s.nextSeq {
			s.nextSeq = event.Sequence + 1
		}
	}
	s.incomplete = incomplete
	s.initialized = true
	return nil
}

func (s *Store) readLocked() (Header, []Event, bool, error) {
	if err := s.initializeLocked(); err != nil {
		return Header{}, nil, false, err
	}
	header, events, incomplete, err := s.readFileLocked()
	if errors.Is(err, os.ErrNotExist) || header.Record == "" {
		return s.header, []Event{}, s.incomplete, nil
	}
	return header, events, incomplete || s.incomplete, err
}

func (s *Store) readFileLocked() (Header, []Event, bool, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Header{}, nil, false, nil
	}
	if err != nil {
		return Header{}, nil, false, fmt.Errorf("open trajectory log: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var header Header
	var events []Event
	lineNumber := 0
	incomplete := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			lineNumber++
			var record struct {
				Record string `json:"record"`
			}
			if jsonErr := json.Unmarshal(line, &record); jsonErr != nil {
				if errors.Is(readErr, io.EOF) {
					incomplete = true
					break
				}
				return Header{}, nil, false, fmt.Errorf("decode trajectory line %d: %w", lineNumber, jsonErr)
			}
			switch record.Record {
			case "header":
				if header.Record != "" {
					return Header{}, nil, false, fmt.Errorf("trajectory contains multiple headers")
				}
				if err := json.Unmarshal(line, &header); err != nil {
					return Header{}, nil, false, fmt.Errorf("decode trajectory header: %w", err)
				}
			case "event":
				var event Event
				if err := json.Unmarshal(line, &event); err != nil {
					return Header{}, nil, false, fmt.Errorf("decode trajectory event: %w", err)
				}
				events = append(events, event)
			default:
				return Header{}, nil, false, fmt.Errorf("unknown trajectory record %q", record.Record)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return Header{}, nil, false, fmt.Errorf("read trajectory log: %w", readErr)
		}
	}
	if header.Record != "" && header.FormatVersion != FormatVersion {
		return Header{}, nil, false, fmt.Errorf("unsupported trajectory format version %d", header.FormatVersion)
	}
	return header, events, incomplete, nil
}

func writeLine(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal trajectory record: %w", err)
	}
	data = append(data, '\n')
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("write trajectory log: %w", err)
	}
	return nil
}

func pageStart(events []Event, turnLimit int) int {
	seen := make(map[string]struct{}, turnLimit)
	start := 0
	for index := len(events) - 1; index >= 0; index-- {
		turnID := events[index].TurnID
		if turnID != "" {
			if _, ok := seen[turnID]; !ok {
				if len(seen) == turnLimit {
					return start
				}
				seen[turnID] = struct{}{}
			}
		}
		start = index
	}
	return start
}
