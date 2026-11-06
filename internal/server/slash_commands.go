package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
)

type slashCommandSettings struct {
	Custom  []domain.SlashCommand `json:"custom"`
	Removed []string              `json:"removed"`
}

var slashName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

func (s *Server) readSlashSettings() (slashCommandSettings, error) {
	var settings slashCommandSettings
	err := s.Store.Get("settings", "slash-commands", &settings)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return settings, err
}

func commandList(settings slashCommandSettings) []domain.SlashCommand {
	commands := domain.BuiltinSlashCommands()
	for i := range commands {
		commands[i].Enabled = !slices.Contains(settings.Removed, commands[i].ID)
	}
	return append(commands, settings.Custom...)
}

func (s *Server) slashCommands() ([]domain.SlashCommand, error) {
	s.slashCommandsMu.Lock()
	defer s.slashCommandsMu.Unlock()
	settings, err := s.readSlashSettings()
	return commandList(settings), err
}

func (s *Server) validateSlashCommand(command *domain.SlashCommand) error {
	command.Name = strings.ToLower(strings.TrimSpace(command.Name))
	command.Description = strings.TrimSpace(command.Description)
	if !slashName.MatchString(command.Name) {
		return errors.New("command names must start with a letter and contain only letters, digits, hyphens or underscores (maximum 64 characters)")
	}
	if len(command.Description) > 1000 {
		return errors.New("description is too long")
	}
	a := &command.Action
	if a.Type == "text" {
		if a.Position == "" {
			a.Position = "insert"
		}
		if !slices.Contains([]string{"insert", "append", "prepend"}, a.Position) || a.Text == "" || len(a.Text) > 1<<20 {
			return errors.New("text commands require text (at most 1 MiB) and an insert, append or prepend position")
		}
		*a = domain.SlashCommandAction{Type: "text", Position: a.Position, Text: a.Text}
		return nil
	}
	if a.Type != "plugin" && a.Type != "app" {
		return errors.New("choose a text, plugin or app action")
	}
	if a.BotID != "" {
		if _, err := s.Bot(a.BotID); err != nil {
			return errors.New("the fixed bot must belong to this machine")
		}
	}
	manifest, err := s.Plugins.Get(a.PluginID)
	if err != nil {
		return errors.New("plugin not found on this machine")
	}
	if a.Type == "plugin" {
		for _, tool := range manifest.Tools {
			if tool.Name != a.Tool {
				continue
			}
			if reason := plugins.StandaloneToolReason(manifest, tool); reason != "" {
				return errors.New(reason)
			}
			if len(a.Arguments) == 0 {
				a.Arguments = json.RawMessage(`{}`)
			}
			if err := plugins.Validate(tool.Parameters, a.Arguments); err != nil {
				return err
			}
			*a = domain.SlashCommandAction{Type: "plugin", PluginID: a.PluginID, Tool: a.Tool, Arguments: a.Arguments, BotID: a.BotID}
			return nil
		}
		return errors.New("tool does not belong to this plugin")
	}
	if manifest.App == nil {
		return errors.New("plugin is not a registered app")
	}
	view, err := plugins.FloatingView(manifest, a.ViewID)
	if err != nil {
		return err
	}
	if a.Feature != "" && !slices.ContainsFunc(view.Features, func(f domain.AppFeature) bool { return f.ID == a.Feature }) {
		return errors.New("app feature is unavailable")
	}
	*a = domain.SlashCommandAction{Type: "app", PluginID: a.PluginID, ViewID: view.ID, Feature: a.Feature, BotID: a.BotID}
	return nil
}

func (s *Server) saveSlashCommand(command domain.SlashCommand, id string) (domain.SlashCommand, error) {
	s.slashCommandsMu.Lock()
	defer s.slashCommandsMu.Unlock()
	settings, err := s.readSlashSettings()
	if err != nil {
		return command, err
	}
	if err = s.validateSlashCommand(&command); err != nil {
		return command, err
	}
	index := -1
	for _, existing := range commandList(settings) {
		if existing.ID == id && existing.Builtin {
			return command, errors.New("built-in definitions cannot be edited")
		}
		if strings.EqualFold(existing.Name, command.Name) && existing.ID != id {
			return command, errors.New("command name is already in use or reserved by a built-in")
		}
	}
	if id != "" {
		index = slices.IndexFunc(settings.Custom, func(c domain.SlashCommand) bool { return c.ID == id })
		if index == -1 {
			return command, errors.New("command not found")
		}
	} else {
		id = domain.ID()
	}
	command.ID, command.Builtin, command.Enabled = id, false, true
	if index >= 0 {
		settings.Custom[index] = command
	} else {
		settings.Custom = append(settings.Custom, command)
	}
	err = s.Store.Put("settings", "slash-commands", settings)
	if err == nil {
		s.emit("changed", "", "", "", nil)
	}
	return command, err
}

func (s *Server) removeSlashCommand(id string, restore bool) error {
	s.slashCommandsMu.Lock()
	defer s.slashCommandsMu.Unlock()
	settings, err := s.readSlashSettings()
	if err != nil {
		return err
	}
	builtin := slices.ContainsFunc(domain.BuiltinSlashCommands(), func(c domain.SlashCommand) bool { return c.ID == id })
	if builtin {
		settings.Removed = slices.DeleteFunc(settings.Removed, func(value string) bool { return value == id })
		if !restore {
			settings.Removed = append(settings.Removed, id)
		}
	} else {
		index := slices.IndexFunc(settings.Custom, func(c domain.SlashCommand) bool { return c.ID == id })
		if index < 0 || restore {
			return errors.New("command not found or not a restorable built-in")
		}
		settings.Custom = slices.Delete(settings.Custom, index, index+1)
	}
	err = s.Store.Put("settings", "slash-commands", settings)
	if err == nil {
		s.emit("changed", "", "", "", nil)
	}
	return err
}

type commandTool struct {
	domain.Tool
	Unavailable string `json:"unavailable,omitempty"`
}
type commandPlugin struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Unavailable string        `json:"unavailable,omitempty"`
	Tools       []commandTool `json:"tools"`
	Views       []domain.View `json:"views"`
}

func (s *Server) slashCommandCatalog() ([]commandPlugin, error) {
	manifests, err := s.Plugins.List()
	catalog := []commandPlugin{}
	for _, manifest := range manifests {
		item := commandPlugin{ID: manifest.ID, Name: manifest.Name, Tools: []commandTool{}, Views: []domain.View{}}
		if manifest.Pending {
			item.Unavailable = "Awaiting plugin permission approval"
		} else if !manifest.Enabled {
			item.Unavailable = "Plugin is disabled"
		}
		for _, tool := range manifest.Tools {
			item.Tools = append(item.Tools, commandTool{tool, plugins.StandaloneToolReason(manifest, tool)})
		}
		if manifest.App != nil {
			for _, view := range manifest.Views {
				if view.Kind != "renderer" {
					view.HTML = ""
					item.Views = append(item.Views, view)
				}
			}
		}
		if len(item.Tools)+len(item.Views) > 0 {
			catalog = append(catalog, item)
		}
	}
	return catalog, err
}

func (s *Server) invokeSlashCommand(w http.ResponseWriter, r *http.Request) {
	var input struct {
		BotID  string `json:"botId"`
		Digest string `json:"digest"`
	}
	if err := decode(w, r, &input); err != nil {
		respond(w, nil, err)
		return
	}
	commands, err := s.slashCommands()
	if err != nil {
		respond(w, nil, err)
		return
	}
	index := slices.IndexFunc(commands, func(c domain.SlashCommand) bool { return c.ID == r.PathValue("id") && c.Enabled })
	if index < 0 || commands[index].Action.Type != "plugin" {
		respond(w, nil, errors.New("enabled plugin command not found"))
		return
	}
	command := commands[index]
	if err = s.validateSlashCommand(&command); err != nil {
		respond(w, nil, err)
		return
	}
	a := command.Action
	if a.BotID != "" {
		input.BotID = a.BotID
	}
	if input.BotID == "" {
		input.BotID = "echo"
	}
	bot, err := s.Bot(input.BotID)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if err := s.requireSharedBot(s.requestAccess(r), bot.ID); err != nil {
		respond(w, nil, err)
		return
	}
	if err := s.requireSharedPlugin(s.requestAccess(r), a.PluginID); err != nil {
		respond(w, nil, err)
		return
	}
	if err := s.requireAccess(s.requestAccess(r), domain.AccessExecute); err != nil {
		respond(w, nil, err)
		return
	}
	ctx, release, err := s.Plugins.Lease(r.Context(), bot, a.PluginID, input.Digest)
	if err != nil {
		respond(w, nil, err)
		return
	}
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.watchPluginNetwork(ctx, cancel, r, bot.ID, a.PluginID)
	value, err := s.Plugins.InvokePlugin(ctx, domain.ExecutionScope{Access: s.requestAccess(r), Bot: bot, Standalone: true}, a.PluginID, a.Tool, a.Arguments)
	respond(w, value, err)
}

func (s *Server) slashCommandRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/slash-commands", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.slashCommands()
		respond(w, s.sharedCommands(s.requestAccess(r), value), err)
	})
	mux.HandleFunc("GET /api/v1/slash-commands/catalog", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.slashCommandCatalog()
		value = slices.DeleteFunc(value, func(p commandPlugin) bool { return s.requireSharedPlugin(s.requestAccess(r), p.ID) != nil })
		respond(w, value, err)
	})
	for _, pattern := range []string{"POST /api/v1/slash-commands", "PUT /api/v1/slash-commands/{id}"} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			var command domain.SlashCommand
			if err := decode(w, r, &command); err != nil {
				respond(w, nil, err)
				return
			}
			value, err := s.saveSlashCommand(command, r.PathValue("id"))
			respond(w, value, err)
		})
	}
	mux.HandleFunc("DELETE /api/v1/slash-commands/{id}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, true, s.removeSlashCommand(r.PathValue("id"), false))
	})
	mux.HandleFunc("POST /api/v1/slash-commands/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		respond(w, true, s.removeSlashCommand(r.PathValue("id"), true))
	})
	mux.HandleFunc("POST /api/v1/slash-commands/{id}/invoke", s.invokeSlashCommand)
}
