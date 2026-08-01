// Package project discovers and resolves projects without inspecting Git state.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
)

const projectIDPrefix = "project-"

// Catalog discovers immediate children of a repository root and combines them
// with the configured Linux custom entries.
type Catalog struct {
	repositoryRoot string
	customEntries  []config.CustomEntry
}

// NewCatalog constructs a catalog from an expanded, validated configuration.
// Filesystem discovery is deferred until List is called.
func NewCatalog(cfg config.Config) (*Catalog, error) {
	const op = "project.new_catalog"

	if err := validateAbsoluteCleanPath(cfg.RepoDirectory); err != nil {
		return nil, domain.FieldError(domain.ErrorCodeConfig, op, "repoDirectory", err.Error())
	}
	if err := validateGotmuxSafe(cfg.RepoDirectory); err != nil {
		return nil, domain.FieldError(domain.ErrorCodeConfig, op, "repoDirectory", err.Error())
	}

	entries := append([]config.CustomEntry(nil), cfg.CustomEntries...)
	for i, entry := range entries {
		field := fmt.Sprintf("customEntries[%d]", i)
		if err := validateName(entry.Name); err != nil {
			return nil, domain.FieldError(domain.ErrorCodeConfig, op, field+".name", err.Error())
		}
		if err := validateAbsoluteCleanPath(entry.Paths.Linux); err != nil {
			return nil, domain.FieldError(domain.ErrorCodeConfig, op, field+".paths.linux", err.Error())
		}
		if err := validateGotmuxSafe(entry.Paths.Linux); err != nil {
			return nil, domain.FieldError(domain.ErrorCodeConfig, op, field+".paths.linux", err.Error())
		}
	}

	return &Catalog{repositoryRoot: cfg.RepoDirectory, customEntries: entries}, nil
}

// RepositoryRoot returns the normalized configured repository root.
func (c *Catalog) RepositoryRoot() string { return c.repositoryRoot }

// List returns a fresh, deterministically sorted snapshot. Repository entries
// are immediate real directories; directory symlinks are not followed.
func (c *Catalog) List(ctx context.Context) ([]domain.Project, error) {
	const op = "project.list"

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(c.repositoryRoot)
	if err != nil {
		code := domain.ErrorCodeInternal
		if errors.Is(err, os.ErrNotExist) {
			code = domain.ErrorCodeNotFound
		}
		return nil, domain.ResourceError(code, op, c.repositoryRoot, "read repository root", err)
	}

	projects := make([]domain.Project, 0, len(entries)+len(c.customEntries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// DirEntry.IsDir reports the directory entry type without following a
		// symlink, preventing discovery from escaping the configured root.
		if !entry.IsDir() {
			continue
		}
		if err := validateName(entry.Name()); err != nil {
			return nil, domain.ResourceError(domain.ErrorCodeInvalidArgument, op, entry.Name(), "repository directory is unsafe: "+err.Error(), nil)
		}
		path := filepath.Join(c.repositoryRoot, entry.Name())
		kind, err := discoveredKind(path)
		if err != nil {
			return nil, domain.ResourceError(domain.ErrorCodeInternal, op, path, "inspect repository kind", err)
		}
		projects = append(projects, newProject(entry.Name(), path, kind))
	}

	for _, entry := range c.customEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		projects = append(projects, newProject(entry.Name, entry.Paths.Linux, domain.ProjectKindCustomEntry))
	}

	if err := validateUniqueProjects(projects); err != nil {
		return nil, err
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Name != projects[j].Name {
			return projects[i].Name < projects[j].Name
		}
		if projects[i].Path != projects[j].Path {
			return projects[i].Path < projects[j].Path
		}
		return projects[i].ID < projects[j].ID
	})
	return projects, nil
}

// Resolve finds a project by stable ID or exact display/window name.
func (c *Catalog) Resolve(ctx context.Context, reference string) (domain.Project, error) {
	const op = "project.resolve"
	if reference == "" {
		return domain.Project{}, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "reference", "must not be empty")
	}
	projects, err := c.List(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	for _, candidate := range projects {
		if candidate.ID == reference || candidate.Name == reference {
			return candidate, nil
		}
	}
	return domain.Project{}, domain.ResourceError(domain.ErrorCodeNotFound, op, reference, "project not found", nil)
}

// ResolveByID finds a project by its stable ID.
func (c *Catalog) ResolveByID(ctx context.Context, id string) (domain.Project, error) {
	return c.resolveExact(ctx, "project.resolve_by_id", "id", id, func(candidate domain.Project) string {
		return candidate.ID
	})
}

// ResolveByName finds a project by its exact display/window name.
func (c *Catalog) ResolveByName(ctx context.Context, name string) (domain.Project, error) {
	return c.resolveExact(ctx, "project.resolve_by_name", "name", name, func(candidate domain.Project) string {
		return candidate.Name
	})
}

func (c *Catalog) resolveExact(ctx context.Context, op, field, value string, key func(domain.Project) string) (domain.Project, error) {
	if value == "" {
		return domain.Project{}, domain.FieldError(domain.ErrorCodeInvalidArgument, op, field, "must not be empty")
	}
	projects, err := c.List(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	for _, candidate := range projects {
		if key(candidate) == value {
			return candidate, nil
		}
	}
	return domain.Project{}, domain.ResourceError(domain.ErrorCodeNotFound, op, value, "project not found", nil)
}

// CreatePath validates a create-project name and returns its destination.
func (c *Catalog) CreatePath(name string) (string, error) {
	return c.destination("project.create_path", "name", name)
}

// ClonePath validates an explicit or URL-derived clone directory name and
// returns its destination.
func (c *Catalog) ClonePath(name string) (string, error) {
	return c.destination("project.clone_path", "directory", name)
}

// WorktreePath validates a worktree directory name and returns its destination.
func (c *Catalog) WorktreePath(name string) (string, error) {
	return c.destination("project.worktree_path", "directory", name)
}

func (c *Catalog) destination(op, field, name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", domain.FieldError(domain.ErrorCodeInvalidArgument, op, field, err.Error())
	}
	return c.validateRepositoryPath(op, filepath.Join(c.repositoryRoot, name))
}

// ValidateRepositoryPath checks that a path remains under the configured root
// both lexically and after resolving every existing symlink prefix. Missing
// final components are allowed so callers can validate destinations before
// creating them.
func (c *Catalog) ValidateRepositoryPath(path string) (string, error) {
	return c.validateRepositoryPath("project.validate_repository_path", path)
}

func (c *Catalog) validateRepositoryPath(op, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", domain.FieldError(domain.ErrorCodeInvalidArgument, op, "path", "must be absolute")
	}
	candidate := filepath.Clean(path)
	if err := validateGotmuxSafe(candidate); err != nil {
		return "", domain.FieldError(domain.ErrorCodeInvalidArgument, op, "path", err.Error())
	}
	if !pathWithin(c.repositoryRoot, candidate) {
		return "", domain.FieldError(domain.ErrorCodeInvalidArgument, op, "path", "must remain under the repository root")
	}

	resolvedRoot, err := filepath.EvalSymlinks(c.repositoryRoot)
	if err != nil {
		return "", domain.ResourceError(domain.ErrorCodeInternal, op, c.repositoryRoot, "resolve repository root", err)
	}
	resolvedCandidate, err := resolveExistingPrefix(candidate)
	if err != nil {
		return "", domain.ResourceError(domain.ErrorCodeInvalidArgument, op, candidate, "resolve destination symlinks", err)
	}
	if !pathWithin(resolvedRoot, resolvedCandidate) {
		return "", domain.FieldError(domain.ErrorCodeInvalidArgument, op, "path", "must remain under the repository root after resolving symlinks")
	}
	return candidate, nil
}

func newProject(name, path string, kind domain.ProjectKind) domain.Project {
	return domain.Project{
		ID:       stableID(path),
		Name:     name,
		Path:     filepath.Clean(path),
		Kind:     kind,
		GitState: domain.GitStateUnknown,
	}
}

func stableID(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return projectIDPrefix + hex.EncodeToString(sum[:])
}

func validateUniqueProjects(projects []domain.Project) error {
	const op = "project.list"
	names := make(map[string]domain.Project, len(projects))
	ids := make(map[string]domain.Project, len(projects))
	for _, candidate := range projects {
		if previous, ok := names[candidate.Name]; ok {
			return domain.ResourceError(domain.ErrorCodeConflict, op, candidate.Name,
				fmt.Sprintf("project name is shared by %q and %q", previous.Path, candidate.Path), nil)
		}
		if previous, ok := ids[candidate.ID]; ok {
			return domain.ResourceError(domain.ErrorCodeConflict, op, candidate.ID,
				fmt.Sprintf("project ID is shared by %q and %q", previous.Path, candidate.Path), nil)
		}
		names[candidate.Name] = candidate
		ids[candidate.ID] = candidate
	}
	for _, namedProject := range projects {
		if identifiedProject, ok := ids[namedProject.Name]; ok && namedProject.ID != identifiedProject.ID {
			return domain.ResourceError(domain.ErrorCodeConflict, op, namedProject.Name,
				fmt.Sprintf("project name for %q collides with the ID for %q", namedProject.Path, identifiedProject.Path), nil)
		}
	}
	return nil
}

func validateName(name string) error {
	if name == "" || strings.TrimSpace(name) == "" {
		return errors.New("must not be empty")
	}
	if name == "." || name == ".." {
		return errors.New("must be a directory name, not a relative path")
	}
	if strings.ContainsAny(name, `/\`) {
		return errors.New("must be a single directory name without path separators")
	}
	if err := validateGotmuxSafe(name); err != nil {
		return err
	}
	return nil
}

func validateGotmuxSafe(value string) error {
	if strings.Contains(value, "-:-") {
		return errors.New("must not contain gotmux's '-:-' query separator")
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("must not contain control characters")
	}
	return nil
}

func discoveredKind(path string) (domain.ProjectKind, error) {
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err == nil && info.Mode().IsRegular() {
		return domain.ProjectKindWorktree, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return domain.ProjectKindRepository, nil
}

func validateAbsoluteCleanPath(path string) error {
	if path == "" {
		return errors.New("must not be empty")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("must be an absolute normalized path")
	}
	return nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func resolveExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0, 2)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
