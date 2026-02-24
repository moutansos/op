#include "configlib.h"
#include "fzflib.h"
#include "pathlib.h"

#include <ctype.h>
#include <dirent.h>
#include <errno.h>
#include <limits.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#define MAIN_TMUX_SESSION_NAME "code"
#define EXIT_KEYWORD "<< Exit >>"
#define CLONE_KEYWORD "<< Clone >>"
#define NEW_REPO_KEYWORD "<< New Repo >>"

typedef struct {
  char **items;
  size_t count;
  size_t capacity;
} StringVec;

typedef struct {
  char *data;
  size_t len;
  size_t cap;
} StringBuilder;

typedef struct {
  const char *name;
  const char *command;
  bool run_in_preferred_shell;
} BuiltinCommand;

static const BuiltinCommand BUILTIN_NEW_WORKTREE = {
    .name = "new-worktree",
    .command = "pwsh \"{{oproot}}/scripts/New-GitWorktree.ps1\"",
    .run_in_preferred_shell = false,
};

static char *xstrdup(const char *value) {
  char *copy;

  if (!value) {
    return NULL;
  }

  copy = strdup(value);
  if (!copy) {
    fprintf(stderr, "Out of memory\n");
  }
  return copy;
}

static void vec_init(StringVec *vec) {
  vec->items = NULL;
  vec->count = 0;
  vec->capacity = 0;
}

static int vec_push(StringVec *vec, const char *value) {
  char **new_items;

  if (vec->count == vec->capacity) {
    size_t new_cap = vec->capacity == 0 ? 8 : vec->capacity * 2;
    if (new_cap < vec->capacity) {
      return 0;
    }

    new_items = realloc(vec->items, new_cap * sizeof(*new_items));
    if (!new_items) {
      return 0;
    }

    vec->items = new_items;
    vec->capacity = new_cap;
  }

  vec->items[vec->count] = xstrdup(value);
  if (!vec->items[vec->count]) {
    return 0;
  }

  vec->count++;
  return 1;
}

static void vec_free(StringVec *vec) {
  size_t i;
  for (i = 0; i < vec->count; ++i) {
    free(vec->items[i]);
  }
  free(vec->items);
  vec->items = NULL;
  vec->count = 0;
  vec->capacity = 0;
}

static int compare_strings(const void *left, const void *right) {
  const char *a = *(const char *const *)left;
  const char *b = *(const char *const *)right;
  return strcmp(a, b);
}

static void sb_init(StringBuilder *sb) {
  sb->data = NULL;
  sb->len = 0;
  sb->cap = 0;
}

static int sb_reserve(StringBuilder *sb, size_t needed) {
  char *new_data;
  size_t new_cap;

  if (needed <= sb->cap) {
    return 1;
  }

  new_cap = sb->cap == 0 ? 128 : sb->cap;
  while (new_cap < needed) {
    if (new_cap > (SIZE_MAX / 2)) {
      return 0;
    }
    new_cap *= 2;
  }

  new_data = realloc(sb->data, new_cap);
  if (!new_data) {
    return 0;
  }

  sb->data = new_data;
  sb->cap = new_cap;
  return 1;
}

static int sb_append_n(StringBuilder *sb, const char *text, size_t text_len) {
  if (!sb_reserve(sb, sb->len + text_len + 1)) {
    return 0;
  }

  memcpy(sb->data + sb->len, text, text_len);
  sb->len += text_len;
  sb->data[sb->len] = '\0';
  return 1;
}

static int sb_append(StringBuilder *sb, const char *text) {
  return sb_append_n(sb, text, strlen(text));
}

static int sb_append_char(StringBuilder *sb, char ch) {
  if (!sb_reserve(sb, sb->len + 2)) {
    return 0;
  }
  sb->data[sb->len++] = ch;
  sb->data[sb->len] = '\0';
  return 1;
}

static char *sb_take(StringBuilder *sb) {
  char *data;
  if (!sb->data) {
    return xstrdup("");
  }
  data = sb->data;
  sb->data = NULL;
  sb->len = 0;
  sb->cap = 0;
  return data;
}

static void sb_free(StringBuilder *sb) {
  free(sb->data);
  sb->data = NULL;
  sb->len = 0;
  sb->cap = 0;
}

static void trim_trailing_newline(char *value) {
  size_t len;
  if (!value) {
    return;
  }

  len = strlen(value);
  while (len > 0 && (value[len - 1] == '\n' || value[len - 1] == '\r')) {
    value[len - 1] = '\0';
    len--;
  }
}

static char *trim_copy(const char *value) {
  const char *start;
  const char *end;
  size_t len;
  char *out;

  if (!value) {
    return NULL;
  }

  start = value;
  while (*start && isspace((unsigned char)*start)) {
    start++;
  }

  end = start + strlen(start);
  while (end > start && isspace((unsigned char)*(end - 1))) {
    end--;
  }

  len = (size_t)(end - start);
  out = malloc(len + 1);
  if (!out) {
    return NULL;
  }
  memcpy(out, start, len);
  out[len] = '\0';
  return out;
}

static char *read_line_prompt(const char *prompt) {
  char *line = NULL;
  size_t line_cap = 0;
  ssize_t line_len;

  if (prompt) {
    printf("%s", prompt);
    fflush(stdout);
  }

  line_len = getline(&line, &line_cap, stdin);
  if (line_len < 0) {
    free(line);
    return NULL;
  }

  trim_trailing_newline(line);
  return line;
}

static int file_is_readable(const char *path) {
  return access(path, R_OK) == 0;
}

static int path_join(StringBuilder *sb, const char *left, const char *right) {
  size_t left_len;

  if (!left || !right) {
    return 0;
  }

  left_len = strlen(left);
  if (!sb_append(sb, left)) {
    return 0;
  }

  if (left_len == 0 || left[left_len - 1] != '/') {
    if (!sb_append_char(sb, '/')) {
      return 0;
    }
  }

  if (right[0] == '/') {
    right++;
  }

  return sb_append(sb, right);
}

static char *join_path(const char *left, const char *right) {
  StringBuilder sb;
  char *joined;
  sb_init(&sb);
  if (!path_join(&sb, left, right)) {
    sb_free(&sb);
    return NULL;
  }
  joined = sb_take(&sb);
  return joined;
}

static char *dirname_copy(const char *path) {
  const char *slash;
  size_t len;
  char *out;

  if (!path) {
    return NULL;
  }

  slash = strrchr(path, '/');
  if (!slash) {
    return xstrdup(".");
  }

  if (slash == path) {
    return xstrdup("/");
  }

  len = (size_t)(slash - path);
  out = malloc(len + 1);
  if (!out) {
    return NULL;
  }

  memcpy(out, path, len);
  out[len] = '\0';
  return out;
}

static char *get_current_executable_path(void) {
  size_t cap = 256;
  char *path = NULL;

  while (1) {
    ssize_t len;
    char *new_path = realloc(path, cap);
    if (!new_path) {
      free(path);
      return NULL;
    }
    path = new_path;

    len = readlink("/proc/self/exe", path, cap - 1);
    if (len < 0) {
      free(path);
      return NULL;
    }

    if ((size_t)len < cap - 1) {
      path[len] = '\0';
      return path;
    }

    if (cap > (SIZE_MAX / 2)) {
      free(path);
      return NULL;
    }
    cap *= 2;
  }
}

static char *resolve_config_path(const char *exe_dir) {
  char *candidate1 = NULL;
  char *candidate2 = NULL;

  if (!exe_dir) {
    return NULL;
  }

  candidate1 = join_path(exe_dir, "../config.json");
  if (candidate1 && file_is_readable(candidate1)) {
    return candidate1;
  }
  free(candidate1);

  candidate2 = join_path(exe_dir, "config.json");
  if (candidate2 && file_is_readable(candidate2)) {
    return candidate2;
  }
  free(candidate2);

  if (file_is_readable("config.json")) {
    return xstrdup("config.json");
  }

  return NULL;
}

static int run_command_in_dir(const char *working_dir, char *const argv[]) {
  pid_t pid;
  int status = 0;

  pid = fork();
  if (pid < 0) {
    perror("fork");
    return -1;
  }

  if (pid == 0) {
    if (working_dir && chdir(working_dir) != 0) {
      perror("chdir");
      _exit(127);
    }

    execvp(argv[0], argv);
    perror("execvp");
    _exit(127);
  }

  while (waitpid(pid, &status, 0) < 0) {
    if (errno != EINTR) {
      perror("waitpid");
      return -1;
    }
  }

  if (WIFEXITED(status)) {
    return WEXITSTATUS(status);
  }

  return -1;
}

static char *capture_command_output(const char *working_dir, char *const argv[],
                                    int *exit_code) {
  int pipefd[2];
  pid_t pid;
  int status = 0;
  StringBuilder sb;

  if (pipe(pipefd) != 0) {
    perror("pipe");
    return NULL;
  }

  pid = fork();
  if (pid < 0) {
    perror("fork");
    close(pipefd[0]);
    close(pipefd[1]);
    return NULL;
  }

  if (pid == 0) {
    close(pipefd[0]);

    if (dup2(pipefd[1], STDOUT_FILENO) < 0) {
      _exit(127);
    }

    close(pipefd[1]);

    if (working_dir && chdir(working_dir) != 0) {
      _exit(127);
    }

    execvp(argv[0], argv);
    _exit(127);
  }

  close(pipefd[1]);
  sb_init(&sb);

  while (1) {
    char chunk[256];
    ssize_t bytes_read = read(pipefd[0], chunk, sizeof(chunk));
    if (bytes_read < 0) {
      if (errno == EINTR) {
        continue;
      }
      sb_free(&sb);
      close(pipefd[0]);
      while (waitpid(pid, &status, 0) < 0 && errno == EINTR) {
      }
      return NULL;
    }

    if (bytes_read == 0) {
      break;
    }

    if (!sb_append_n(&sb, chunk, (size_t)bytes_read)) {
      sb_free(&sb);
      close(pipefd[0]);
      while (waitpid(pid, &status, 0) < 0 && errno == EINTR) {
      }
      return NULL;
    }
  }

  close(pipefd[0]);

  while (waitpid(pid, &status, 0) < 0) {
    if (errno != EINTR) {
      sb_free(&sb);
      return NULL;
    }
  }

  if (exit_code) {
    *exit_code = WIFEXITED(status) ? WEXITSTATUS(status) : -1;
  }

  return sb_take(&sb);
}

static int output_has_visible_text(const char *text) {
  const unsigned char *ptr = (const unsigned char *)text;
  if (!ptr) {
    return 0;
  }
  while (*ptr) {
    if (!isspace(*ptr)) {
      return 1;
    }
    ptr++;
  }
  return 0;
}

static void update_repo_if_clean(const char *repo_path, bool no_repo_update) {
  char *git_dir;
  struct stat statbuf;

  if (no_repo_update) {
    return;
  }

  git_dir = join_path(repo_path, ".git");
  if (!git_dir) {
    return;
  }

  if (stat(git_dir, &statbuf) != 0 || !S_ISDIR(statbuf.st_mode)) {
    printf("Repo %s is not a git repo\n", repo_path);
    free(git_dir);
    return;
  }

  free(git_dir);

  {
    char *const status_argv[] = {"git", "status", "--porcelain", NULL};
    int status_code = 0;
    char *status_output = capture_command_output(repo_path, status_argv, &status_code);

    if (!status_output || status_code != 0) {
      fprintf(stderr, "Failed to check git status for %s\n", repo_path);
      free(status_output);
      return;
    }

    if (output_has_visible_text(status_output)) {
      printf("Repo %s has uncommitted changes\n", repo_path);
      free(status_output);
      return;
    }
    free(status_output);
  }

  printf("Updating repo %s\n", repo_path);
  {
    char *const pull_argv[] = {"git", "pull", NULL};
    (void)run_command_in_dir(repo_path, pull_argv);
  }
}

static char *quote_for_posix_single(const char *text) {
  StringBuilder sb;
  const char *ptr;

  sb_init(&sb);
  if (!sb_append_char(&sb, '\'')) {
    sb_free(&sb);
    return NULL;
  }

  for (ptr = text; *ptr; ++ptr) {
    if (*ptr == '\'') {
      if (!sb_append(&sb, "'\\''")) {
        sb_free(&sb);
        return NULL;
      }
    } else {
      if (!sb_append_char(&sb, *ptr)) {
        sb_free(&sb);
        return NULL;
      }
    }
  }

  if (!sb_append_char(&sb, '\'')) {
    sb_free(&sb);
    return NULL;
  }

  return sb_take(&sb);
}

static char *quote_for_powershell_single(const char *text) {
  StringBuilder sb;
  const char *ptr;

  sb_init(&sb);
  if (!sb_append_char(&sb, '\'')) {
    sb_free(&sb);
    return NULL;
  }

  for (ptr = text; *ptr; ++ptr) {
    if (*ptr == '\'') {
      if (!sb_append(&sb, "''")) {
        sb_free(&sb);
        return NULL;
      }
    } else {
      if (!sb_append_char(&sb, *ptr)) {
        sb_free(&sb);
        return NULL;
      }
    }
  }

  if (!sb_append_char(&sb, '\'')) {
    sb_free(&sb);
    return NULL;
  }

  return sb_take(&sb);
}

static char *quote_for_double(const char *text) {
  StringBuilder sb;
  const char *ptr;

  sb_init(&sb);
  if (!sb_append_char(&sb, '"')) {
    sb_free(&sb);
    return NULL;
  }

  for (ptr = text; *ptr; ++ptr) {
    if (*ptr == '\\' || *ptr == '"' || *ptr == '$' || *ptr == '`') {
      if (!sb_append_char(&sb, '\\')) {
        sb_free(&sb);
        return NULL;
      }
    }
    if (!sb_append_char(&sb, *ptr)) {
      sb_free(&sb);
      return NULL;
    }
  }

  if (!sb_append_char(&sb, '"')) {
    sb_free(&sb);
    return NULL;
  }

  return sb_take(&sb);
}

static int shell_name_is_powershell(const char *shell_name) {
  const char *base;

  if (!shell_name || !*shell_name) {
    return 0;
  }

  base = strrchr(shell_name, '/');
  base = base ? base + 1 : shell_name;

  return strcmp(base, "pwsh") == 0 || strcmp(base, "pwsh.exe") == 0 ||
         strcmp(base, "powershell") == 0 ||
         strcmp(base, "powershell.exe") == 0;
}

static int shell_name_is_posix_interactive(const char *shell_name) {
  const char *base;

  if (!shell_name || !*shell_name) {
    return 0;
  }

  base = strrchr(shell_name, '/');
  base = base ? base + 1 : shell_name;

  return strcmp(base, "bash") == 0 || strcmp(base, "zsh") == 0;
}

static char *build_shell_cd_command(const char *shell_name, const char *path_to_cd) {
  char *quoted_path;
  StringBuilder sb;

  if (shell_name_is_powershell(shell_name)) {
    quoted_path = quote_for_powershell_single(path_to_cd);
  } else {
    quoted_path = quote_for_posix_single(path_to_cd);
  }

  if (!quoted_path) {
    return NULL;
  }

  sb_init(&sb);
  if (!sb_append(&sb, "cd ")) {
    free(quoted_path);
    sb_free(&sb);
    return NULL;
  }

  if (!sb_append(&sb, quoted_path)) {
    free(quoted_path);
    sb_free(&sb);
    return NULL;
  }

  free(quoted_path);
  return sb_take(&sb);
}

static char *replace_all(const char *input, const char *needle,
                         const char *replacement) {
  StringBuilder sb;
  size_t needle_len;
  size_t replacement_len;
  const char *cursor;

  if (!input || !needle || !replacement) {
    return NULL;
  }

  needle_len = strlen(needle);
  replacement_len = strlen(replacement);
  if (needle_len == 0) {
    return xstrdup(input);
  }

  sb_init(&sb);
  cursor = input;

  while (*cursor) {
    const char *match = strstr(cursor, needle);
    if (!match) {
      if (!sb_append(&sb, cursor)) {
        sb_free(&sb);
        return NULL;
      }
      break;
    }

    if (!sb_append_n(&sb, cursor, (size_t)(match - cursor))) {
      sb_free(&sb);
      return NULL;
    }

    if (replacement_len > 0 && !sb_append_n(&sb, replacement, replacement_len)) {
      sb_free(&sb);
      return NULL;
    }

    cursor = match + needle_len;
  }

  return sb_take(&sb);
}

static int run_shell_command_in_dir(const char *working_dir, const char *command) {
  char *const argv[] = {"/bin/sh", "-lc", (char *)command, NULL};
  return run_command_in_dir(working_dir, argv);
}

static int invoke_here_in_preferred_shell(const char *preferred_shell,
                                          const char *command,
                                          const char *location) {
  if (!preferred_shell || preferred_shell[0] == '\0') {
    return run_shell_command_in_dir(location, command);
  }

  if (shell_name_is_powershell(preferred_shell)) {
    char *const argv[] = {(char *)preferred_shell, "-NoExit", "-c", (char *)command,
                          NULL};
    return run_command_in_dir(location, argv);
  }

  if (shell_name_is_posix_interactive(preferred_shell)) {
    StringBuilder wrapped;
    char *wrapped_cmd;
    int status;

    sb_init(&wrapped);
    if (!sb_append(&wrapped, command) || !sb_append(&wrapped, " || true; exec ") ||
        !sb_append(&wrapped, preferred_shell) || !sb_append(&wrapped, " -i")) {
      sb_free(&wrapped);
      return -1;
    }

    wrapped_cmd = sb_take(&wrapped);
    if (!wrapped_cmd) {
      return -1;
    }

    {
      char *const argv[] = {(char *)preferred_shell, "-i", "-c", wrapped_cmd, NULL};
      status = run_command_in_dir(location, argv);
    }

    free(wrapped_cmd);
    return status;
  }

  return run_shell_command_in_dir(location, command);
}

static int open_preferred_shell_in_dir(const char *preferred_shell,
                                       const char *location) {
  const char *shell = preferred_shell;

  if (!shell || shell[0] == '\0') {
    shell = getenv("SHELL");
    if (!shell || shell[0] == '\0') {
      shell = "/bin/bash";
    }
  }

  {
    char *const argv[] = {(char *)shell, NULL};
    return run_command_in_dir(location, argv);
  }
}

static int tmux_has_session(const char *session_name) {
  char *const argv[] = {"tmux", "has-session", "-t", (char *)session_name, NULL};
  return run_command_in_dir(NULL, argv) == 0;
}

static int ensure_tmux_session(const char *session_name, const char *start_dir) {
  if (tmux_has_session(session_name)) {
    return 1;
  }

  {
    char *const argv[] = {"tmux", "new-session", "-d", "-s", (char *)session_name,
                          "-n", "op", "-c", (char *)start_dir, NULL};
    return run_command_in_dir(NULL, argv) == 0;
  }
}

static int parse_int_with_default(const char *value, int fallback) {
  char *endptr;
  long parsed;

  if (!value) {
    return fallback;
  }

  errno = 0;
  parsed = strtol(value, &endptr, 10);
  if (errno != 0 || endptr == value) {
    return fallback;
  }

  while (*endptr) {
    if (!isspace((unsigned char)*endptr)) {
      return fallback;
    }
    endptr++;
  }

  if (parsed < INT_MIN || parsed > INT_MAX) {
    return fallback;
  }

  return (int)parsed;
}

static int get_tmux_base_index(void) {
  char *const argv[] = {"tmux", "show-options", "-gv", "base-index", NULL};
  int status = 0;
  char *output = capture_command_output(NULL, argv, &status);
  int base_index = 0;

  if (!output || status != 0) {
    free(output);
    return 0;
  }

  trim_trailing_newline(output);
  base_index = parse_int_with_default(output, 0);
  free(output);
  return base_index;
}

static int int_vec_contains(const int *values, size_t count, int target) {
  size_t i;
  for (i = 0; i < count; ++i) {
    if (values[i] == target) {
      return 1;
    }
  }
  return 0;
}

static int get_next_tmux_window_index(const char *session_name) {
  char *const argv[] = {"tmux", "list-windows", "-t", (char *)session_name,
                        "-F", "#{window_index}", NULL};
  int status = 0;
  char *output = capture_command_output(NULL, argv, &status);
  int *indexes = NULL;
  size_t count = 0;
  size_t cap = 0;
  int candidate;
  char *cursor;

  if (!output || status != 0) {
    free(output);
    return get_tmux_base_index();
  }

  cursor = output;
  while (*cursor) {
    char *line_start = cursor;
    char *line_end = strchr(cursor, '\n');
    int value;

    if (line_end) {
      *line_end = '\0';
      cursor = line_end + 1;
    } else {
      cursor += strlen(cursor);
    }

    trim_trailing_newline(line_start);
    value = parse_int_with_default(line_start, INT_MIN);
    if (value != INT_MIN) {
      int *new_indexes;
      if (count == cap) {
        size_t new_cap = cap == 0 ? 8 : cap * 2;
        if (new_cap < cap) {
          free(indexes);
          free(output);
          return get_tmux_base_index();
        }

        new_indexes = realloc(indexes, new_cap * sizeof(*new_indexes));
        if (!new_indexes) {
          free(indexes);
          free(output);
          return get_tmux_base_index();
        }

        indexes = new_indexes;
        cap = new_cap;
      }

      indexes[count++] = value;
    }
  }

  candidate = get_tmux_base_index();
  while (int_vec_contains(indexes, count, candidate)) {
    candidate++;
  }

  free(indexes);
  free(output);
  return candidate;
}

static char *get_tmux_default_shell(void) {
  char *const argv[] = {"tmux", "show-options", "-gv", "default-shell", NULL};
  int status = 0;
  char *output = capture_command_output(NULL, argv, &status);

  if (!output || status != 0) {
    free(output);
    return xstrdup("sh");
  }

  trim_trailing_newline(output);
  if (output[0] == '\0') {
    free(output);
    return xstrdup("sh");
  }

  return output;
}

static char *tmux_capture_single_line(const char *target, const char *format,
                                      const char *subcommand) {
  char *const argv[] = {"tmux", (char *)subcommand, "-P", "-F", (char *)format,
                        "-t", (char *)target, "-v", NULL};
  int status = 0;
  char *output = capture_command_output(NULL, argv, &status);
  if (!output || status != 0) {
    free(output);
    return NULL;
  }
  trim_trailing_newline(output);
  if (output[0] == '\0') {
    free(output);
    return NULL;
  }
  return output;
}

static char *tmux_display_single_line(const char *target, const char *format) {
  char *const argv[] = {"tmux", "display-message", "-p", "-t", (char *)target,
                        (char *)format, NULL};
  int status = 0;
  char *output = capture_command_output(NULL, argv, &status);
  if (!output || status != 0) {
    free(output);
    return NULL;
  }
  trim_trailing_newline(output);
  if (output[0] == '\0') {
    free(output);
    return NULL;
  }
  return output;
}

static int tmux_send_keys(const char *target, const char *keys) {
  char *const argv[] = {"tmux", "send-keys", "-t", (char *)target, (char *)keys,
                        "C-m", NULL};
  return run_command_in_dir(NULL, argv) == 0;
}

static int tmux_resize_pane(const char *pane_id, int rows) {
  char rows_buf[32];
  char *const argv[] = {"tmux", "resize-pane", "-t", (char *)pane_id, "-y",
                        rows_buf, NULL};
  snprintf(rows_buf, sizeof(rows_buf), "%d", rows);
  return run_command_in_dir(NULL, argv) == 0;
}

static int tmux_select_pane(const char *pane_id) {
  char *const argv[] = {"tmux", "select-pane", "-t", (char *)pane_id, NULL};
  return run_command_in_dir(NULL, argv) == 0;
}

static int create_tmux_window(const char *session_name, int target_index,
                              const char *window_name, int *new_window_index) {
  char target_buf[128];
  char *const argv[] = {"tmux", "new-window", "-P", "-F", "#{window_index}",
                        "-d", "-t", target_buf, "-n", (char *)window_name, NULL};
  int status = 0;
  char *output;

  snprintf(target_buf, sizeof(target_buf), "%s:%d", session_name, target_index);
  output = capture_command_output(NULL, argv, &status);
  if (!output || status != 0) {
    free(output);
    return 0;
  }

  trim_trailing_newline(output);
  *new_window_index = parse_int_with_default(output, INT_MIN);
  free(output);

  return *new_window_index != INT_MIN;
}

static void start_tmux_shell_pane(const char *repo_open_path, int window_index,
                                  const char *preferred_shell) {
  char target[128];
  char *existing_pane_id;
  char *new_pane_id;
  char *cd_command;

  if (!preferred_shell || preferred_shell[0] == '\0') {
    return;
  }

  snprintf(target, sizeof(target), "%s:%d", MAIN_TMUX_SESSION_NAME, window_index);
  existing_pane_id = tmux_display_single_line(target, "#{pane_id}");
  if (!existing_pane_id) {
    return;
  }

  new_pane_id = tmux_capture_single_line(target, "#{pane_id}", "split-window");
  if (!new_pane_id) {
    free(existing_pane_id);
    return;
  }

  if (!tmux_send_keys(new_pane_id, preferred_shell)) {
    free(existing_pane_id);
    free(new_pane_id);
    return;
  }

  if (shell_name_is_powershell(preferred_shell)) {
    usleep(500000);
  }

  cd_command = build_shell_cd_command(preferred_shell, repo_open_path);
  if (!cd_command) {
    free(existing_pane_id);
    free(new_pane_id);
    return;
  }

  (void)tmux_send_keys(new_pane_id, cd_command);
  (void)tmux_send_keys(new_pane_id, "clear");
  (void)tmux_resize_pane(new_pane_id, 20);
  (void)tmux_select_pane(existing_pane_id);

  free(cd_command);
  free(existing_pane_id);
  free(new_pane_id);
}

static int build_directory_listing(const char *directory, StringVec *output) {
  DIR *dir;
  struct dirent *entry;

  vec_init(output);

  dir = opendir(directory);
  if (!dir) {
    perror("opendir");
    return 0;
  }

  while ((entry = readdir(dir)) != NULL) {
    if (strcmp(entry->d_name, ".") == 0 || strcmp(entry->d_name, "..") == 0) {
      continue;
    }

    if (!vec_push(output, entry->d_name)) {
      closedir(dir);
      vec_free(output);
      return 0;
    }
  }

  closedir(dir);
  if (output->count > 1) {
    qsort(output->items, output->count, sizeof(*output->items), compare_strings);
  }

  return 1;
}

static char *build_fzf_input_from_vec(const StringVec *vec) {
  size_t i;
  StringBuilder sb;

  sb_init(&sb);
  for (i = 0; i < vec->count; ++i) {
    if (!sb_append(&sb, vec->items[i]) || !sb_append_char(&sb, '\n')) {
      sb_free(&sb);
      return NULL;
    }
  }

  return sb_take(&sb);
}

static const OpCustomEntry *find_custom_entry_by_name(const OpConfig *config,
                                                       const char *name) {
  size_t i;
  for (i = 0; i < config->custom_entry_count; ++i) {
    if (config->custom_entries[i].name &&
        strcmp(config->custom_entries[i].name, name) == 0) {
      return &config->custom_entries[i];
    }
  }
  return NULL;
}

static const OpCustomCommand *find_custom_command_by_name(const OpConfig *config,
                                                           const char *name) {
  size_t i;
  for (i = 0; i < config->custom_command_count; ++i) {
    if (config->custom_commands[i].name &&
        strcmp(config->custom_commands[i].name, name) == 0) {
      return &config->custom_commands[i];
    }
  }
  return NULL;
}

static int execute_named_command(const char *command_template, bool run_in_preferred_shell,
                                 const char *repo_open_path, const char *op_root,
                                 const char *preferred_shell) {
  char *path_quoted = NULL;
  char *command_with_path = NULL;
  char *final_command = NULL;
  int status;

  path_quoted = quote_for_double(repo_open_path);
  if (!path_quoted) {
    return -1;
  }

  command_with_path = replace_all(command_template, "{{path}}", path_quoted);
  free(path_quoted);
  if (!command_with_path) {
    return -1;
  }

  final_command = replace_all(command_with_path, "{{oproot}}", op_root);
  free(command_with_path);
  if (!final_command) {
    return -1;
  }

  if (run_in_preferred_shell) {
    status = invoke_here_in_preferred_shell(preferred_shell, final_command,
                                            repo_open_path);
  } else {
    status = run_shell_command_in_dir(repo_open_path, final_command);
  }

  free(final_command);
  return status;
}

static int usage(const char *prog) {
  fprintf(stderr,
          "Usage: %s [--continuous|-c] [--no-repo-update]\n",
          prog ? prog : "op-native");
  return 1;
}

int main(int argc, char **argv) {
  bool continuous = false;
  bool no_repo_update = false;
  int argi;

  char *executable_path = NULL;
  char *executable_dir = NULL;
  char *config_path = NULL;
  OpConfig *config = NULL;
  char *repo_dir_expanded = NULL;
  char *repo_dir_abs = NULL;
  char *op_root = NULL;
  char *rerun_with_repo = NULL;

  for (argi = 1; argi < argc; ++argi) {
    if (strcmp(argv[argi], "--continuous") == 0 ||
        strcmp(argv[argi], "-c") == 0) {
      continuous = true;
    } else if (strcmp(argv[argi], "--no-repo-update") == 0) {
      no_repo_update = true;
    } else if (strcmp(argv[argi], "--help") == 0 ||
               strcmp(argv[argi], "-h") == 0) {
      return usage(argv[0]);
    } else {
      fprintf(stderr, "Unknown argument: %s\n", argv[argi]);
      return usage(argv[0]);
    }
  }

  executable_path = get_current_executable_path();
  if (!executable_path) {
    fprintf(stderr, "Unable to determine executable path\n");
    return 1;
  }

  executable_dir = dirname_copy(executable_path);
  if (!executable_dir) {
    fprintf(stderr, "Unable to determine executable directory\n");
    free(executable_path);
    return 1;
  }

  config_path = resolve_config_path(executable_dir);
  if (!config_path) {
    fprintf(stderr,
            "Unable to locate config.json (tried executable dir and parent dir)\n");
    free(executable_path);
    free(executable_dir);
    return 1;
  }

  config = loadConfigs(config_path);
  if (!config) {
    free(executable_path);
    free(executable_dir);
    free(config_path);
    return 1;
  }

  repo_dir_expanded = expand_tilde(config->repo_directory);
  if (!repo_dir_expanded) {
    fprintf(stderr, "Failed to expand repo directory: %s\n", config->repo_directory);
    free(executable_path);
    free(executable_dir);
    free(config_path);
    freeConfig(config);
    return 1;
  }

  repo_dir_abs = make_absolute_path(repo_dir_expanded);
  free(repo_dir_expanded);
  if (!repo_dir_abs) {
    fprintf(stderr, "Failed to build absolute repo directory path\n");
    free(executable_path);
    free(executable_dir);
    free(config_path);
    freeConfig(config);
    return 1;
  }

  op_root = dirname_copy(config_path);
  if (!op_root) {
    fprintf(stderr, "Failed to derive op root from config path\n");
    free(executable_path);
    free(executable_dir);
    free(config_path);
    free(repo_dir_abs);
    freeConfig(config);
    return 1;
  }

  while (1) {
    StringVec options;
    StringVec action_options;
    char *options_input = NULL;
    char *selected_repo_raw = NULL;
    char *selected_repo = NULL;
    char *repo_open_path = NULL;
    char *action_input = NULL;
    char *selected_action = NULL;
    const OpCustomEntry *custom_entry;

    vec_init(&options);
    vec_init(&action_options);

    if (!build_directory_listing(repo_dir_abs, &options)) {
      fprintf(stderr, "Failed to list repo directory '%s'\n", repo_dir_abs);
      goto loop_cleanup;
    }

    if (!vec_push(&options, CLONE_KEYWORD) || !vec_push(&options, NEW_REPO_KEYWORD)) {
      fprintf(stderr, "Out of memory\n");
      goto loop_cleanup;
    }

    {
      size_t i;
      for (i = 0; i < config->custom_entry_count; ++i) {
        if (config->custom_entries[i].name &&
            !vec_push(&options, config->custom_entries[i].name)) {
          fprintf(stderr, "Out of memory\n");
          goto loop_cleanup;
        }
      }
    }

    if (continuous && !vec_push(&options, EXIT_KEYWORD)) {
      fprintf(stderr, "Out of memory\n");
      goto loop_cleanup;
    }

    if (rerun_with_repo && rerun_with_repo[0] != '\0') {
      selected_repo_raw = rerun_with_repo;
      rerun_with_repo = NULL;
    } else {
      options_input = build_fzf_input_from_vec(&options);
      if (!options_input) {
        fprintf(stderr, "Out of memory\n");
        goto loop_cleanup;
      }
      selected_repo_raw = askChoicesWithPrompt(options_input, "op native > ");
    }

    if (!selected_repo_raw || selected_repo_raw[0] == '\0') {
      free(selected_repo_raw);
      selected_repo_raw = NULL;
      goto loop_cleanup;
    }

    selected_repo = trim_copy(selected_repo_raw);
    free(selected_repo_raw);
    selected_repo_raw = NULL;

    if (!selected_repo || selected_repo[0] == '\0') {
      goto loop_cleanup;
    }

    if (strcmp(selected_repo, EXIT_KEYWORD) == 0) {
      goto loop_exit;
    }

    if (strcmp(selected_repo, CLONE_KEYWORD) == 0) {
      char *repo_to_clone = read_line_prompt("Enter the repo to clone: ");
      if (repo_to_clone && repo_to_clone[0] != '\0') {
        char *const clone_argv[] = {"git", "-C", repo_dir_abs, "clone", repo_to_clone,
                                    NULL};
        (void)run_command_in_dir(NULL, clone_argv);
      }
      free(repo_to_clone);
      goto loop_cleanup;
    }

    if (strcmp(selected_repo, NEW_REPO_KEYWORD) == 0) {
      char *repo_to_create = read_line_prompt("Enter the name of the repo to create: ");
      if (!repo_to_create || repo_to_create[0] == '\0') {
        printf("Error: repo name is required.\n");
        free(repo_to_create);
        goto loop_cleanup;
      }

      repo_open_path = join_path(repo_dir_abs, repo_to_create);
      if (!repo_open_path) {
        fprintf(stderr, "Out of memory\n");
        free(repo_to_create);
        goto loop_cleanup;
      }

      if (mkdir(repo_open_path, 0775) != 0 && errno != EEXIST) {
        perror("mkdir");
        free(repo_to_create);
        goto loop_cleanup;
      }

      {
        char *const init_argv[] = {"git", "-C", repo_open_path, "init", NULL};
        (void)run_command_in_dir(NULL, init_argv);
      }

      free(rerun_with_repo);
      rerun_with_repo = xstrdup(repo_to_create);
      free(repo_to_create);
      goto loop_cleanup;
    }

    custom_entry = find_custom_entry_by_name(config, selected_repo);
    if (custom_entry) {
      char *custom_path_expanded;
      if (!custom_entry->linux_path) {
        fprintf(stderr, "Custom entry '%s' has no linux path\n", selected_repo);
        goto loop_cleanup;
      }

      custom_path_expanded = expand_tilde(custom_entry->linux_path);
      if (!custom_path_expanded) {
        fprintf(stderr, "Failed to expand custom entry path for '%s'\n", selected_repo);
        goto loop_cleanup;
      }

      repo_open_path = make_absolute_path(custom_path_expanded);
      free(custom_path_expanded);
      if (!repo_open_path) {
        fprintf(stderr, "Failed to build absolute path for custom entry '%s'\n",
                selected_repo);
        goto loop_cleanup;
      }
    } else {
      repo_open_path = join_path(repo_dir_abs, selected_repo);
      if (!repo_open_path) {
        fprintf(stderr, "Out of memory\n");
        goto loop_cleanup;
      }
    }

    if (!vec_push(&action_options, "nvim-tmux") || !vec_push(&action_options, "nvim") ||
        !vec_push(&action_options, "cd-here")) {
      fprintf(stderr, "Out of memory\n");
      goto loop_cleanup;
    }

    if (!config->is_server && !vec_push(&action_options, "code")) {
      fprintf(stderr, "Out of memory\n");
      goto loop_cleanup;
    }

    {
      size_t i;
      for (i = 0; i < config->custom_command_count; ++i) {
        if (config->custom_commands[i].name &&
            !vec_push(&action_options, config->custom_commands[i].name)) {
          fprintf(stderr, "Out of memory\n");
          goto loop_cleanup;
        }
      }
    }

    if (!vec_push(&action_options, BUILTIN_NEW_WORKTREE.name)) {
      fprintf(stderr, "Out of memory\n");
      goto loop_cleanup;
    }

    action_input = build_fzf_input_from_vec(&action_options);
    if (!action_input) {
      fprintf(stderr, "Out of memory\n");
      goto loop_cleanup;
    }

    selected_action = askChoices(action_input);
    if (!selected_action || selected_action[0] == '\0') {
      goto loop_cleanup;
    }

    if (strcmp(selected_action, "nvim") == 0) {
      char *const nvim_argv[] = {"nvim", repo_open_path, NULL};
      update_repo_if_clean(repo_open_path, no_repo_update);
      (void)run_command_in_dir(NULL, nvim_argv);
    } else if (strcmp(selected_action, "code") == 0) {
      char *const code_argv[] = {"code", repo_open_path, NULL};
      (void)run_command_in_dir(NULL, code_argv);
    } else if (strcmp(selected_action, "cd-here") == 0) {
      (void)open_preferred_shell_in_dir(config->preferred_shell, repo_open_path);
    } else if (strcmp(selected_action, "nvim-tmux") == 0) {
      int target_window_index;
      int new_window_index;
      char target_window[128];
      char *main_shell = NULL;
      char *main_cd_command = NULL;

      update_repo_if_clean(repo_open_path, no_repo_update);

      if (!ensure_tmux_session(MAIN_TMUX_SESSION_NAME, repo_dir_abs)) {
        fprintf(stderr, "Failed to ensure tmux session '%s'\n",
                MAIN_TMUX_SESSION_NAME);
        goto loop_cleanup;
      }

      target_window_index = get_next_tmux_window_index(MAIN_TMUX_SESSION_NAME);
      if (!create_tmux_window(MAIN_TMUX_SESSION_NAME, target_window_index,
                              selected_repo, &new_window_index)) {
        fprintf(stderr, "Failed to create tmux window for %s\n", selected_repo);
        goto loop_cleanup;
      }

      main_shell = get_tmux_default_shell();
      if (!main_shell) {
        fprintf(stderr, "Failed to read tmux default shell\n");
        goto loop_cleanup;
      }

      main_cd_command = build_shell_cd_command(main_shell, repo_open_path);
      if (!main_cd_command) {
        free(main_shell);
        fprintf(stderr, "Failed to build cd command for tmux\n");
        goto loop_cleanup;
      }

      snprintf(target_window, sizeof(target_window), "%s:%d", MAIN_TMUX_SESSION_NAME,
               new_window_index);
      (void)tmux_send_keys(target_window, main_cd_command);
      (void)tmux_send_keys(target_window, "nvim .");
      start_tmux_shell_pane(repo_open_path, new_window_index, config->preferred_shell);
      printf("Opening nvim in tmux session '%s'\n", MAIN_TMUX_SESSION_NAME);

      free(main_shell);
      free(main_cd_command);
    } else {
      const OpCustomCommand *custom_command =
          find_custom_command_by_name(config, selected_action);

      if (custom_command) {
        (void)execute_named_command(custom_command->command,
                                    custom_command->run_in_preferred_shell,
                                    repo_open_path, op_root, config->preferred_shell);
      } else if (strcmp(selected_action, BUILTIN_NEW_WORKTREE.name) == 0) {
        (void)execute_named_command(BUILTIN_NEW_WORKTREE.command,
                                    BUILTIN_NEW_WORKTREE.run_in_preferred_shell,
                                    repo_open_path, op_root, config->preferred_shell);
      } else {
        printf("No option selected\n");
      }
    }

  loop_cleanup:
    free(options_input);
    free(selected_repo_raw);
    free(selected_repo);
    free(repo_open_path);
    free(action_input);
    free(selected_action);
    vec_free(&options);
    vec_free(&action_options);

    if (!continuous) {
      break;
    }

    continue;

  loop_exit:
    free(options_input);
    free(selected_repo_raw);
    free(selected_repo);
    free(repo_open_path);
    free(action_input);
    free(selected_action);
    vec_free(&options);
    vec_free(&action_options);
    break;
  }

  free(rerun_with_repo);
  free(op_root);
  free(repo_dir_abs);
  freeConfig(config);
  free(config_path);
  free(executable_dir);
  free(executable_path);

  return 0;
}
