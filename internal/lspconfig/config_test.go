package lspconfig

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/appdata"
)

func testProfile(id, language string) Profile {
	return Profile{
		ID: id, Name: strings.ToUpper(id), Command: id,
		Args:                  []string{"--global"},
		Selectors:             []DocumentSelector{{LanguageID: language, Extensions: []string{"." + language}}},
		Environment:           map[string]string{"GLOBAL": "true"},
		InitializationOptions: map[string]any{"global": true},
		Settings:              map[string]any{"global": true},
	}
}

func TestProfileValidationAndDuplicateIDs(t *testing.T) {
	valid := testProfile("server", "go")
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid profile: %v", err)
	}
	for name, mutate := range map[string]func(*Profile){
		"invalid id":       func(profile *Profile) { profile.ID = "Bad ID" },
		"missing command":  func(profile *Profile) { profile.Command = "" },
		"missing selector": func(profile *Profile) { profile.Selectors = nil },
		"empty match": func(profile *Profile) {
			profile.Selectors = []DocumentSelector{{LanguageID: "go"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			profile := valid.Clone()
			mutate(&profile)
			if err := profile.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if _, err := NormalizeProfiles([]Profile{valid, valid}); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate ID error, got %v", err)
	}
}

func TestTemplatesAreCloned(t *testing.T) {
	first, ok := TemplateByID("gopls")
	if !ok {
		t.Fatal("gopls template missing")
	}
	first.Selectors[0].Extensions[0] = ".changed"
	second, _ := TemplateByID("gopls")
	if second.Selectors[0].Extensions[0] != ".go" {
		t.Fatalf("template was mutated: %+v", second)
	}
}

func TestTemplatesCanBeSaved(t *testing.T) {
	for _, template := range Templates() {
		t.Run(template.ID, func(t *testing.T) {
			store := NewStore(appdata.NewStore(filepath.Join(t.TempDir(), "echo.json")))
			profile, ok := TemplateByID(template.ID)
			if !ok {
				t.Fatal("template missing")
			}
			if _, err := store.Add(profile); err != nil {
				t.Fatalf("create profile from template: %v", err)
			}
			profiles, err := store.Load()
			if err != nil || len(profiles) != 1 {
				t.Fatalf("load saved template: profiles=%+v err=%v", profiles, err)
			}
			if profiles[0].ID != template.ID {
				t.Fatalf("saved profile ID = %q, want %q", profiles[0].ID, template.ID)
			}
		})
	}
}

func TestProfileSelectorFileMatches(t *testing.T) {
	for _, field := range []string{"extensions", "filenames"} {
		t.Run(field, func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				value   string
				wantErr bool
			}{
				{name: "letter x", value: ".cxx"},
				{name: "digit zero", value: "file0"},
				{name: "forward slash", value: "dir/file", wantErr: true},
				{name: "backslash", value: `dir\file`, wantErr: true},
				{name: "null byte", value: "file\x00name", wantErr: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					profile := testProfile("server", "cpp")
					selector := DocumentSelector{LanguageID: "cpp"}
					if field == "extensions" {
						selector.Extensions = []string{tc.value}
					} else {
						selector.Filenames = []string{tc.value}
					}
					profile.Selectors = []DocumentSelector{selector}
					if err := profile.Validate(); (err != nil) != tc.wantErr {
						t.Fatalf("Validate() for %q = %v, want error = %v", tc.value, err, tc.wantErr)
					}
				})
			}
		})
	}
}

func TestWorkspaceOverridesReplaceFieldsAndDetectOverlap(t *testing.T) {
	goProfile := testProfile("go-one", "go")
	otherGo := testProfile("go-two", "go")
	args := []string{}
	environment := map[string]string{"WORKSPACE": "true"}
	settings := map[string]any{"workspace": true}
	config := WorkspaceConfig{
		EnabledProfileIDs: []string{"go-one"},
		Overrides: map[string]ProfileOverride{"go-one": {
			Args: &args, Environment: &environment, Settings: &settings,
		}},
	}
	effective, err := EffectiveProfiles(config, []Profile{goProfile, otherGo})
	if err != nil {
		t.Fatalf("effective profiles: %v", err)
	}
	if len(effective[0].Args) != 0 || effective[0].Environment["GLOBAL"] != "" || effective[0].Settings["global"] != nil {
		t.Fatalf("override was merged instead of replaced: %+v", effective[0])
	}
	if effective[0].Environment["WORKSPACE"] != "true" || effective[0].Settings["workspace"] != true {
		t.Fatalf("workspace override missing: %+v", effective[0])
	}
	config.EnabledProfileIDs = []string{"go-one", "go-two"}
	if err := config.Validate([]Profile{goProfile, otherGo}); err == nil || !strings.Contains(err.Error(), "handled by both") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestStorePreservesOtherAppDataAndSerializesConcurrentAdds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "echo.json")
	data := appdata.NewStore(path)
	settings := json.RawMessage(`{"theme":"dark"}`)
	if err := data.Save(appdata.File{Settings: settings, Workspaces: []appdata.Workspace{{ID: "workspace"}}}); err != nil {
		t.Fatal(err)
	}
	store := NewStore(data)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := store.Add(testProfile("server-"+string(rune('a'+index)), "lang-"+string(rune('a'+index))))
			errors <- err
		}(index)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent add: %v", err)
		}
	}
	profiles, err := store.Load()
	if err != nil || len(profiles) != 8 {
		t.Fatalf("load profiles: count=%d err=%v", len(profiles), err)
	}
	file, err := data.Load()
	if err != nil {
		t.Fatal(err)
	}
	var preserved map[string]any
	if err := json.Unmarshal(file.Settings, &preserved); err != nil || preserved["theme"] != "dark" || len(file.Workspaces) != 1 {
		t.Fatalf("other app data was changed: settings=%s workspaces=%+v err=%v", file.Settings, file.Workspaces, err)
	}
}

func TestCheckedProfileUpdateRejectsWorkspaceConflictWithoutPersisting(t *testing.T) {
	store := NewStore(appdata.NewStore(filepath.Join(t.TempDir(), "echo.json")))
	first, second := testProfile("first", "go"), testProfile("second", "python")
	if _, err := store.Add(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(second); err != nil {
		t.Fatal(err)
	}
	second.Selectors = []DocumentSelector{{LanguageID: "go", Extensions: []string{".go2"}}}
	config := WorkspaceConfig{EnabledProfileIDs: []string{"first", "second"}}
	if _, err := store.UpdateChecked("second", second, func(profiles []Profile) error {
		return config.Validate(profiles)
	}); err == nil || !strings.Contains(err.Error(), "handled by both") {
		t.Fatalf("expected workspace overlap error, got %v", err)
	}
	profiles, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.ID == "second" && profile.Selectors[0].LanguageID != "python" {
			t.Fatalf("rejected profile update was persisted: %+v", profile)
		}
	}
}
