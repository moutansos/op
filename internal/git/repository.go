package git

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/moutansos/op/internal/domain"
)

// Command is the complete, shell-free description of a git invocation.
type Command struct {
	Directory string
	Name      string
	Args      []string
}

// CommandRunner is the narrow process boundary used by Repository.
type CommandRunner interface {
	Run(context.Context, Command) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Directory
	return cmd.CombinedOutput()
}

// Repository performs local git operations without invoking a shell.
type Repository struct {
	runner CommandRunner
}

func NewRepository() *Repository {
	return NewRepositoryWithRunner(execCommandRunner{})
}

func NewRepositoryWithRunner(runner CommandRunner) *Repository {
	if runner == nil {
		panic("git: nil command runner")
	}
	return &Repository{runner: runner}
}

type CloneOptions struct {
	URL             string
	ParentDirectory string
	Directory       string
}

type CloneResult struct {
	Directory string
	Path      string
}

// Clone validates the remote and clones it into one direct child of ParentDirectory.
func (r *Repository) Clone(ctx context.Context, options CloneOptions) (CloneResult, error) {
	const op = "git.clone"
	if err := validateCloneURL(options.URL); err != nil {
		return CloneResult{}, err
	}
	if err := validateAbsoluteDirectory(op, "parentDirectory", options.ParentDirectory); err != nil {
		return CloneResult{}, err
	}

	directory := options.Directory
	if directory == "" {
		var err error
		directory, err = CloneDirectoryName(options.URL)
		if err != nil {
			return CloneResult{}, err
		}
	}
	if err := validateChildName(op, "directory", directory); err != nil {
		return CloneResult{}, err
	}
	destination := filepath.Join(options.ParentDirectory, directory)
	if exists, err := pathExists(destination); err != nil {
		return CloneResult{}, domain.ResourceError(domain.ErrorCodeInternal, op, destination, "inspect clone destination", err)
	} else if exists {
		return CloneResult{}, domain.ResourceError(domain.ErrorCodeAlreadyExists, op, destination, "clone destination already exists", nil)
	}
	// Creating the destination atomically establishes ownership for cleanup.
	// Git accepts an existing empty directory as a clone destination.
	if err := os.Mkdir(destination, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return CloneResult{}, domain.ResourceError(domain.ErrorCodeAlreadyExists, op, destination, "clone destination already exists", err)
		}
		return CloneResult{}, domain.ResourceError(domain.ErrorCodeInternal, op, destination, "create clone destination", err)
	}
	ownedDestination, err := os.Lstat(destination)
	if err != nil {
		_ = os.RemoveAll(destination)
		return CloneResult{}, domain.ResourceError(domain.ErrorCodeInternal, op, destination, "record clone destination ownership", err)
	}

	output, err := r.runner.Run(ctx, Command{
		Directory: options.ParentDirectory,
		Name:      "git",
		Args:      []string{"clone", "--", options.URL, directory},
	})
	if err != nil {
		cloneErr := commandError(ctx, op, destination, "clone repository", output, err)
		if cleanupErr := removeOwnedDirectory(destination, ownedDestination); cleanupErr != nil {
			return CloneResult{}, domain.ResourceError(domain.ErrorCodeInternal, op, destination, "clone failed and its destination could not be removed", errors.Join(cloneErr, cleanupErr))
		}
		return CloneResult{}, cloneErr
	}
	return CloneResult{Directory: directory, Path: destination}, nil
}

// CloneDirectoryName derives the checkout directory from a supported clone URL.
func CloneDirectoryName(rawURL string) (string, error) {
	const op = "git.clone_name"
	if err := validateCloneURL(rawURL); err != nil {
		return "", err
	}

	path := cloneURLPath(rawURL)
	path = strings.TrimRight(path, "/")
	name := path[strings.LastIndex(path, "/")+1:]
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	name = strings.TrimSuffix(name, ".git")
	if err := validateChildName(op, "url", name); err != nil {
		return "", domain.FieldError(domain.ErrorCodeInvalidArgument, op, "url", "clone URL does not contain a valid repository name")
	}
	return name, nil
}

// Init initializes path as a repository. Git creates the final directory when needed.
func (r *Repository) Init(ctx context.Context, path string) error {
	const op = "git.init"
	if err := validateAbsoluteDirectory(op, "path", path); err != nil {
		return err
	}
	if exists, err := pathExists(path); err != nil {
		return domain.ResourceError(domain.ErrorCodeInternal, op, path, "inspect repository destination", err)
	} else if exists {
		return domain.ResourceError(domain.ErrorCodeAlreadyExists, op, path, "repository destination already exists", nil)
	}
	parent, name := filepath.Dir(path), filepath.Base(path)
	output, err := r.runner.Run(ctx, Command{
		Directory: parent,
		Name:      "git",
		Args:      []string{"init", "--", name},
	})
	if err != nil {
		return commandError(ctx, op, path, "initialize repository", output, err)
	}
	return nil
}

// State reports worktree cleanliness using git's stable porcelain format.
// This also works when .git is a file, as it is in linked worktrees.
func (r *Repository) State(ctx context.Context, path string) (domain.GitState, error) {
	const op = "git.status"
	if err := validateAbsoluteDirectory(op, "path", path); err != nil {
		return domain.GitStateUnknown, err
	}
	output, err := r.runner.Run(ctx, Command{
		Directory: path,
		Name:      "git",
		Args:      []string{"status", "--porcelain"},
	})
	if err != nil {
		if isNotRepository(output, err) {
			return domain.GitStateNotRepository, nil
		}
		return domain.GitStateUnknown, commandError(ctx, op, path, "inspect repository state", output, err)
	}
	if len(output) == 0 {
		return domain.GitStateClean, nil
	}
	return domain.GitStateDirty, nil
}

// Pull updates a repository only when its worktree is clean.
func (r *Repository) Pull(ctx context.Context, path string) error {
	const op = "git.pull"
	state, err := r.State(ctx, path)
	if err != nil {
		return err
	}
	switch state {
	case domain.GitStateNotRepository:
		return domain.ResourceError(domain.ErrorCodeNotFound, op, path, "path is not a git worktree", nil)
	case domain.GitStateDirty:
		return domain.ResourceError(domain.ErrorCodeConflict, op, path, "worktree has uncommitted changes", nil)
	}

	output, err := r.runner.Run(ctx, Command{
		Directory: path,
		Name:      "git",
		Args:      []string{"pull", "--ff-only"},
	})
	if err != nil {
		return commandError(ctx, op, path, "pull repository", output, err)
	}
	return nil
}

type WorktreeOptions struct {
	Repository string
	Branch     string
	Directory  string
}

type WorktreeResult struct {
	Branch    string
	Directory string
	Path      string
}

// CreateWorktree creates a new branch and sibling worktree in one git command.
func (r *Repository) CreateWorktree(ctx context.Context, options WorktreeOptions) (WorktreeResult, error) {
	const op = "git.worktree_create"
	if err := validateAbsoluteDirectory(op, "repository", options.Repository); err != nil {
		return WorktreeResult{}, err
	}
	if err := validateBranch(options.Branch); err != nil {
		return WorktreeResult{}, err
	}

	directory := options.Directory
	if directory == "" {
		directory = filepath.Base(options.Repository) + "-" + strings.ReplaceAll(options.Branch, "/", "-")
	}
	if err := validateChildName(op, "directory", directory); err != nil {
		return WorktreeResult{}, err
	}
	destination := filepath.Join(filepath.Dir(options.Repository), directory)
	if destination == options.Repository {
		return WorktreeResult{}, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "directory", "worktree must be a sibling of the repository")
	}
	if exists, err := pathExists(destination); err != nil {
		return WorktreeResult{}, domain.ResourceError(domain.ErrorCodeInternal, op, destination, "inspect worktree destination", err)
	} else if exists {
		return WorktreeResult{}, domain.ResourceError(domain.ErrorCodeAlreadyExists, op, destination, "worktree destination already exists", nil)
	}

	output, err := r.runner.Run(ctx, Command{
		Directory: options.Repository,
		Name:      "git",
		Args:      []string{"worktree", "add", "-b", options.Branch, "--", destination},
	})
	if err != nil {
		return WorktreeResult{}, commandError(ctx, op, destination, "create branch and worktree", output, err)
	}
	return WorktreeResult{Branch: options.Branch, Directory: directory, Path: destination}, nil
}

func validateCloneURL(rawURL string) error {
	const op = "git.clone"
	if rawURL == "" || rawURL != strings.TrimSpace(rawURL) || containsControl(rawURL) {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "url", "must be a non-empty HTTPS, SSH, or SCP-style URL")
	}
	if isSCPLikeURL(rawURL) {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "url", "must be a valid clone URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "ssh" {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "url", "scheme must be https or ssh")
	}
	if parsed.Host == "" || strings.Trim(parsed.Path, "/") == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "url", "must include a host and repository path without a query or fragment")
	}
	return nil
}

func isSCPLikeURL(value string) bool {
	if strings.Contains(value, "://") || strings.ContainsAny(value, "\\ ") {
		return false
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 || colon == len(value)-1 {
		return false
	}
	host := value[:colon]
	path := value[colon+1:]
	at := strings.LastIndexByte(host, '@')
	if at < 0 && isReservedURLScheme(host) {
		return false
	}
	if at >= 0 {
		if at == 0 || at == len(host)-1 {
			return false
		}
		host = host[at+1:]
	}
	return host != "" && !strings.ContainsAny(host, "/:") && strings.Trim(path, "/") != ""
}

func isReservedURLScheme(value string) bool {
	switch strings.ToLower(value) {
	case "file", "ftp", "git", "http", "https", "ssh":
		return true
	default:
		return false
	}
}

func cloneURLPath(rawURL string) string {
	if isSCPLikeURL(rawURL) {
		return rawURL[strings.IndexByte(rawURL, ':')+1:]
	}
	parsed, _ := url.Parse(rawURL)
	return parsed.EscapedPath()
}

func validateAbsoluteDirectory(op, field, path string) error {
	if path == "" || !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, field, "must be an absolute normalized path")
	}
	return nil
}

func validateChildName(op, field, name string) error {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) || containsControl(name) {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, field, "must be a single directory name")
	}
	return nil
}

func validateBranch(branch string) error {
	const op = "git.worktree_create"
	invalid := branch == "" || branch != strings.TrimSpace(branch) || strings.HasPrefix(branch, "-") ||
		strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") ||
		strings.ContainsAny(branch, " ~^:?*[\\") || containsControl(branch)
	if invalid {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "branch", "must be a valid branch name")
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func removeOwnedDirectory(path string, owned os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(owned, current) {
		return nil
	}
	return os.RemoveAll(path)
}

func isNotRepository(output []byte, err error) bool {
	message := strings.ToLower(string(output) + " " + err.Error())
	return strings.Contains(message, "not a git repository")
}

func commandError(ctx context.Context, op, resource, action string, output []byte, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return domain.ResourceError(domain.CodeOf(ctxErr), op, resource, action, ctxErr)
	}
	code := domain.ErrorCodeInternal
	if errors.Is(err, exec.ErrNotFound) {
		code = domain.ErrorCodeDependency
	}
	detail := strings.TrimSpace(string(output))
	if detail != "" {
		action += ": " + detail
	}
	return domain.ResourceError(code, op, resource, action, err)
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
