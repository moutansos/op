package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
)

func TestCatalogListsImmediateDirectoriesAndLinuxCustomEntries(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "zeta"))
	mustMkdir(t, filepath.Join(root, "alpha"))
	mustMkdir(t, filepath.Join(root, "alpha", "nested"))
	mustWrite(t, filepath.Join(root, "plain-file"))
	customPath := filepath.Join(t.TempDir(), "custom-linux")

	catalog := newTestCatalog(t, root, []config.CustomEntry{{
		Name: "dotfiles",
		Paths: config.EntryPaths{
			Linux:   customPath,
			Windows: `C:\Users\tester\dotfiles`,
		},
	}})
	projects, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if got, want := projectNames(projects), []string{"alpha", "dotfiles", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project names = %v, want %v", got, want)
	}
	if projects[1].Path != customPath || projects[1].Kind != domain.ProjectKindCustomEntry {
		t.Fatalf("Linux custom entry = %#v", projects[1])
	}
	for _, candidate := range projects {
		if candidate.GitState != domain.GitStateUnknown {
			t.Fatalf("Git state for %q = %q, want unknown", candidate.Name, candidate.GitState)
		}
		if candidate.Name != "dotfiles" && candidate.Kind != domain.ProjectKindRepository {
			t.Fatalf("repository project = %#v", candidate)
		}
	}
}

func TestCatalogDoesNotFollowDirectorySymlinks(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	outside := t.TempDir()
	mustMkdir(t, inside)
	mustMkdir(t, filepath.Join(outside, "escaped-child"))
	mustSymlink(t, inside, filepath.Join(root, "inside-link"))
	mustSymlink(t, outside, filepath.Join(root, "outside-link"))
	mustSymlink(t, filepath.Join(root, "missing"), filepath.Join(root, "broken-link"))

	projects, err := newTestCatalog(t, root, nil).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projectNames(projects), []string{"inside"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("projects = %v, want %v", got, want)
	}
}

func TestCatalogPersistsWorktreeKindFromGitFile(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	worktree := filepath.Join(root, "linked-worktree")
	mustMkdir(t, filepath.Join(repository, ".git"))
	mustMkdir(t, worktree)
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: ../repository/.git/worktrees/linked-worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	projects, err := newTestCatalog(t, root, nil).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kinds := make(map[string]domain.ProjectKind, len(projects))
	for _, candidate := range projects {
		kinds[candidate.Name] = candidate.Kind
	}
	if kinds["repository"] != domain.ProjectKindRepository || kinds["linked-worktree"] != domain.ProjectKindWorktree {
		t.Fatalf("catalog kinds = %#v", kinds)
	}
}

func TestCatalogIDsAreStableAndSortingIsDeterministic(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"same-prefix-2", "same-prefix-1", "Beta", "alpha"} {
		mustMkdir(t, filepath.Join(root, name))
	}

	first := newTestCatalog(t, root, nil)
	firstProjects, err := first.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondProjects, err := newTestCatalog(t, root, nil).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstProjects, secondProjects) {
		t.Fatalf("catalog snapshots differ:\nfirst:  %#v\nsecond: %#v", firstProjects, secondProjects)
	}
	if got, want := projectNames(firstProjects), []string{"Beta", "alpha", "same-prefix-1", "same-prefix-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project names = %v, want %v", got, want)
	}
	for _, candidate := range firstProjects {
		if !strings.HasPrefix(candidate.ID, projectIDPrefix) || len(candidate.ID) != len(projectIDPrefix)+64 {
			t.Fatalf("unexpected stable ID %q", candidate.ID)
		}
	}

	before := idsByName(firstProjects)
	mustMkdir(t, filepath.Join(root, "aardvark"))
	after, err := first.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range before {
		if idsByName(after)[name] != id {
			t.Fatalf("ID for %q changed after adding another directory", name)
		}
	}
}

func TestCatalogResolvesByIDAndExactName(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "CaseSensitive"))
	catalog := newTestCatalog(t, root, nil)
	projects, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := projects[0]

	for description, resolve := range map[string]func() (domain.Project, error){
		"combined ID":   func() (domain.Project, error) { return catalog.Resolve(context.Background(), want.ID) },
		"combined name": func() (domain.Project, error) { return catalog.Resolve(context.Background(), want.Name) },
		"ID":            func() (domain.Project, error) { return catalog.ResolveByID(context.Background(), want.ID) },
		"name":          func() (domain.Project, error) { return catalog.ResolveByName(context.Background(), want.Name) },
	} {
		t.Run(description, func(t *testing.T) {
			got, err := resolve()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("resolved project = %#v, want %#v", got, want)
			}
		})
	}

	for _, reference := range []string{"casesensitive", "missing"} {
		_, err := catalog.Resolve(context.Background(), reference)
		if err == nil || !domain.IsCode(err, domain.ErrorCodeNotFound) {
			t.Fatalf("Resolve(%q) error = %v, want not_found", reference, err)
		}
	}
}

func TestCatalogRejectsAmbiguousProjects(t *testing.T) {
	t.Run("duplicate name", func(t *testing.T) {
		root := t.TempDir()
		mustMkdir(t, filepath.Join(root, "shared"))
		catalog := newTestCatalog(t, root, []config.CustomEntry{{
			Name:  "shared",
			Paths: config.EntryPaths{Linux: filepath.Join(t.TempDir(), "elsewhere")},
		}})
		assertConflict(t, catalog)
	})

	t.Run("duplicate ID from path", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(t.TempDir(), "same-path")
		catalog := newTestCatalog(t, root, []config.CustomEntry{
			{Name: "one", Paths: config.EntryPaths{Linux: path}},
			{Name: "two", Paths: config.EntryPaths{Linux: path}},
		})
		assertConflict(t, catalog)
	})

	t.Run("name collides with another ID", func(t *testing.T) {
		root := t.TempDir()
		mustMkdir(t, filepath.Join(root, "repository"))
		repositoryID := stableID(filepath.Join(root, "repository"))
		catalog := newTestCatalog(t, root, []config.CustomEntry{{
			Name:  repositoryID,
			Paths: config.EntryPaths{Linux: filepath.Join(t.TempDir(), "custom")},
		}})
		assertConflict(t, catalog)
	})
}

func TestDestinationNameValidation(t *testing.T) {
	root := t.TempDir()
	catalog := newTestCatalog(t, root, nil)
	valid := []string{"repo", "repo with spaces", ".dotfiles", "feature-123"}
	for _, name := range valid {
		for operation, destination := range destinationFunctions(catalog) {
			t.Run(operation+"/valid/"+name, func(t *testing.T) {
				got, err := destination(name)
				if err != nil {
					t.Fatal(err)
				}
				if want := filepath.Join(root, name); got != want {
					t.Fatalf("destination = %q, want %q", got, want)
				}
			})
		}
	}

	invalid := []string{"", "   ", ".", "..", "../escape", "nested/repo", `..\escape`, "line\nbreak", "gotmux-:-query"}
	for _, name := range invalid {
		for operation, destination := range destinationFunctions(catalog) {
			t.Run(operation+"/invalid", func(t *testing.T) {
				_, err := destination(name)
				if err == nil || !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
					t.Fatalf("destination(%q) error = %v, want invalid_argument", name, err)
				}
			})
		}
	}
}

func TestRepositoryPathContainment(t *testing.T) {
	root := t.TempDir()
	catalog := newTestCatalog(t, root, nil)

	t.Run("safe missing descendants", func(t *testing.T) {
		path := filepath.Join(root, "missing", "child", "repository")
		got, err := catalog.ValidateRepositoryPath(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != path {
			t.Fatalf("validated path = %q, want %q", got, path)
		}
	})

	t.Run("lexical escape", func(t *testing.T) {
		path := filepath.Join(root, "..", "outside", "repository")
		_, err := catalog.ValidateRepositoryPath(path)
		if err == nil || !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
			t.Fatalf("error = %v, want invalid_argument", err)
		}
	})

	t.Run("existing escaped symlink", func(t *testing.T) {
		outside := t.TempDir()
		mustSymlink(t, outside, filepath.Join(root, "escape"))
		_, err := catalog.ValidateRepositoryPath(filepath.Join(root, "escape"))
		if err == nil || !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
			t.Fatalf("error = %v, want invalid_argument", err)
		}
	})

	t.Run("missing child below escaped symlink", func(t *testing.T) {
		outside := t.TempDir()
		mustSymlink(t, outside, filepath.Join(root, "escape-parent"))
		_, err := catalog.ValidateRepositoryPath(filepath.Join(root, "escape-parent", "missing", "repository"))
		if err == nil || !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
			t.Fatalf("error = %v, want invalid_argument", err)
		}
	})

	t.Run("safe internal symlink", func(t *testing.T) {
		target := filepath.Join(root, "target")
		mustMkdir(t, target)
		mustSymlink(t, target, filepath.Join(root, "alias"))
		path := filepath.Join(root, "alias", "missing")
		if _, err := catalog.ValidateRepositoryPath(path); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("configured root symlink", func(t *testing.T) {
		realRoot := t.TempDir()
		mustMkdir(t, filepath.Join(realRoot, "existing-repository"))
		linkParent := t.TempDir()
		linkRoot := filepath.Join(linkParent, "repos")
		mustSymlink(t, realRoot, linkRoot)
		linkedCatalog := newTestCatalog(t, linkRoot, nil)
		projects, err := linkedCatalog.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got, want := projectNames(projects), []string{"existing-repository"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("projects through root symlink = %v, want %v", got, want)
		}
		if _, err := linkedCatalog.CreatePath("new-repository"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCatalogHonorsCanceledContext(t *testing.T) {
	catalog := newTestCatalog(t, t.TempDir(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := catalog.List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestNewCatalogRejectsUnnormalizedConfigurationPaths(t *testing.T) {
	root := t.TempDir()
	for _, cfg := range []config.Config{
		{RepoDirectory: "relative"},
		{RepoDirectory: root + string(filepath.Separator) + "."},
		{RepoDirectory: root, CustomEntries: []config.CustomEntry{{Name: "custom", Paths: config.EntryPaths{Linux: "relative"}}}},
	} {
		_, err := NewCatalog(cfg)
		if err == nil || !domain.IsCode(err, domain.ErrorCodeConfig) {
			t.Fatalf("NewCatalog(%#v) error = %v, want config error", cfg, err)
		}
	}
}

func TestNewCatalogRejectsGotmuxDangerousNamesAndPaths(t *testing.T) {
	root := t.TempDir()
	for name, cfg := range map[string]config.Config{
		"repository root": {RepoDirectory: filepath.Join(root, "unsafe-:-root")},
		"custom name": {
			RepoDirectory: root,
			CustomEntries: []config.CustomEntry{{Name: "unsafe-:-name", Paths: config.EntryPaths{Linux: filepath.Join(root, "custom")}}},
		},
		"custom path": {
			RepoDirectory: root,
			CustomEntries: []config.CustomEntry{{Name: "custom", Paths: config.EntryPaths{Linux: filepath.Join(root, "unsafe-:-path")}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewCatalog(cfg)
			if !domain.IsCode(err, domain.ErrorCodeConfig) {
				t.Fatalf("NewCatalog() error = %v, want config", err)
			}
		})
	}
}

func TestCatalogRejectsDangerousDiscoveredName(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "unsafe-:-repository"))
	_, err := newTestCatalog(t, root, nil).List(context.Background())
	if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
		t.Fatalf("List() error = %v, want invalid_argument", err)
	}
}

func newTestCatalog(t *testing.T, root string, entries []config.CustomEntry) *Catalog {
	t.Helper()
	catalog, err := NewCatalog(config.Config{RepoDirectory: root, CustomEntries: entries})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func assertConflict(t *testing.T, catalog *Catalog) {
	t.Helper()
	_, err := catalog.List(context.Background())
	if err == nil || !domain.IsCode(err, domain.ErrorCodeConflict) {
		t.Fatalf("List() error = %v, want conflict", err)
	}
}

func destinationFunctions(catalog *Catalog) map[string]func(string) (string, error) {
	return map[string]func(string) (string, error){
		"create":   catalog.CreatePath,
		"clone":    catalog.ClonePath,
		"worktree": catalog.WorktreePath,
	}
}

func projectNames(projects []domain.Project) []string {
	names := make([]string, len(projects))
	for i, candidate := range projects {
		names[i] = candidate.Name
	}
	return names
}

func idsByName(projects []domain.Project) map[string]string {
	ids := make(map[string]string, len(projects))
	for _, candidate := range projects {
		ids[candidate.Name] = candidate.ID
	}
	return ids
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("not a project"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatal(err)
	}
}
