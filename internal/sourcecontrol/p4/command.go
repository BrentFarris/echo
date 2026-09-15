// Package p4 integrates the installed Helix CLI without emulating a Git index.
package p4

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

const commandLimit = 16 << 20

var errOutputLimit = errors.New("P4 output limit exceeded; results are incomplete")

type record map[string]string

// JSON diagnostics are records too. Keep successful records on partial errors.
func parseRecords(data []byte) ([]record, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var records []record
	var diagnostics []string
	for {
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return records, fmt.Errorf("invalid P4 JSON: %w", err)
		}
		r := record{}
		for key, value := range raw {
			r[key] = fmt.Sprint(value)
		}
		// -Mj replaces invalid source bytes. Such paths must never be executed.
		for key, value := range r {
			if (strings.Contains(strings.ToLower(key), "file") || key == "path" || key == "clientRoot") && strings.ContainsRune(value, utf8.RuneError) {
				return records, errors.New("P4 returned a filename that cannot be decoded losslessly")
			}
		}
		if r["code"] == "error" || r["severity"] == "3" || r["severity"] == "4" {
			diagnostics = append(diagnostics, strings.TrimSpace(r["data"]))
		} else {
			records = append(records, r)
		}
	}
	if len(diagnostics) > 0 {
		return records, errors.New(strings.Join(diagnostics, "\n"))
	}
	return records, nil
}

type boundedBuffer struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.max-b.Len() {
		b.exceeded = true
		return 0, errors.New("P4 output limit exceeded")
	}
	return b.Buffer.Write(data)
}

type connection struct {
	Unicode        bool   `json:"unicode,omitempty"`
	CommandCharset string `json:"commandCharset,omitempty"`
	Directory      string `json:"directory"`
	Server         string `json:"server"`
	User           string `json:"user"`
	Client         string `json:"client"`
	Charset        string `json:"charset,omitempty"`
}

func (p *Provider) command(ctx context.Context, c connection, input []byte, tagged bool, args ...string) ([]byte, error) {
	if p.commandHook != nil {
		if err := p.commandHook(args); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	global := []string{}
	if c.Server != "" {
		global = append(global, "-p", c.Server)
	}
	if c.User != "" {
		global = append(global, "-u", c.User)
	}
	if c.Client != "" {
		global = append(global, "-c", c.Client)
	}
	if c.Charset != "" {
		global = append(global, "-C", c.Charset)
	}
	if c.CommandCharset != "" {
		global = append(global, "-Q", c.CommandCharset)
	}
	if tagged {
		global = append(global, "-ztag", "-Mj")
	}
	if input != nil && (len(args) == 0 || args[len(args)-1] != "-i") {
		global = append(global, "-x", "-")
	}
	global = append(global, args...)
	cmd := exec.CommandContext(ctx, p.binary, global...)
	cmd.Dir = c.Directory
	cmd.WaitDelay = time.Second
	hideWindow(cmd)
	if p.env != nil {
		cmd.Env = p.env
	}
	cmd.Stdin = bytes.NewReader(input)
	stdout := &boundedBuffer{max: commandLimit}
	stderr := &boundedBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.Bytes(), fmt.Errorf("P4 command cancelled or timed out: %w", ctx.Err())
	}
	if stdout.exceeded || stderr.exceeded {
		return stdout.Bytes(), errOutputLimit
	}
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("P4: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), err
}

func (p *Provider) run(ctx context.Context, c connection, args ...string) ([]record, error) {
	data, commandErr := p.command(ctx, c, nil, true, args...)
	rows, parseErr := parseRecords(data)
	if parseErr != nil {
		return rows, parseErr
	}
	return rows, emptyResultError(rows, commandErr)
}

func emptyResultError(rows []record, err error) error {
	if err != nil && len(rows) > 0 {
		benign := true
		for _, row := range rows {
			if row["generic"] != "17" || row["severity"] != "2" {
				benign = false
			}
		}
		if benign {
			return nil
		}
	}
	return err
}
func (p *Provider) runInput(ctx context.Context, c connection, args, values []string) ([]record, error) {
	if len(values) == 0 {
		return nil, nil
	}
	data, err := p.command(ctx, c, []byte(strings.Join(values, "\n")+"\n"), true, args...)
	rows, parseErr := parseRecords(data)
	if parseErr != nil {
		return rows, parseErr
	}
	return rows, emptyResultError(rows, err)
}

// Exact arguments never include P4 wildcard or revision syntax. Depot paths
// returned by the server are already escaped; local paths must be escaped once.
func fileArg(path string) string {
	return strings.NewReplacer("%", "%25", "@", "%40", "#", "%23", "*", "%2A").Replace(path)
}

func (p *Provider) files(ctx context.Context, c connection, args []string, paths []string) ([]record, error) {
	var result []record
	// 'add -f' and reconcile take literal local names, unlike commands addressing
	// existing depot revisions. Native ignore checking remains enabled.
	forceAdd := len(args) > 0 && args[0] == "add"
	literal := forceAdd || (len(args) > 0 && args[0] == "reconcile")
	if forceAdd {
		args = append(append([]string{}, args...), "-f")
	}
	for start := 0; start < len(paths); start += 64 {
		end := min(start+64, len(paths))
		batch := []string{}
		for _, path := range paths[start:end] {
			if strings.ContainsAny(path, "\x00\r\n") || strings.Contains(path, "...") || strings.ContainsRune(path, utf8.RuneError) {
				return result, errors.New("unsupported P4 filename")
			}
			if literal {
				batch = append(batch, path)
			} else {
				batch = append(batch, fileArg(path))
			}
		}
		rows, err := p.runInput(ctx, c, args, batch)
		result = append(result, rows...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
