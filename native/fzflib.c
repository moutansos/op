#include "fzflib.h"

#include <errno.h>
#include <limits.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

char *askChoicesWithPrompt(const char *choices, const char *prompt) {
  int to_child[2];
  int from_child[2];
  pid_t pid;
  char *output = NULL;
  size_t output_len = 0;
  size_t output_cap = 0;
  int status = 0;

  if (!choices) {
    return NULL;
  }

  if (pipe(to_child) != 0) {
    perror("pipe");
    return NULL;
  }

  if (pipe(from_child) != 0) {
    perror("pipe");
    close(to_child[0]);
    close(to_child[1]);
    return NULL;
  }

  pid = fork();
  if (pid < 0) {
    perror("fork");
    close(to_child[0]);
    close(to_child[1]);
    close(from_child[0]);
    close(from_child[1]);
    return NULL;
  }

  if (pid == 0) {
    close(to_child[1]);
    close(from_child[0]);

    if (dup2(to_child[0], STDIN_FILENO) < 0) {
      _exit(127);
    }

    if (dup2(from_child[1], STDOUT_FILENO) < 0) {
      _exit(127);
    }

    close(to_child[0]);
    close(from_child[1]);

    if (prompt && prompt[0] != '\0') {
      execlp("fzf", "fzf", "--prompt", prompt, (char *)NULL);
    } else {
      execlp("fzf", "fzf", (char *)NULL);
    }
    _exit(127);
  }

  close(to_child[0]);
  close(from_child[1]);

  {
    size_t remaining = strlen(choices);
    const char *ptr = choices;
    while (remaining > 0) {
      ssize_t written = write(to_child[1], ptr, remaining);
      if (written < 0) {
        if (errno == EINTR) {
          continue;
        }
        break;
      }
      ptr += (size_t)written;
      remaining -= (size_t)written;
    }
  }
  close(to_child[1]);

  while (1) {
    char chunk[256];
    ssize_t bytes_read = read(from_child[0], chunk, sizeof(chunk));
    if (bytes_read < 0) {
      if (errno == EINTR) {
        continue;
      }
      break;
    }
    if (bytes_read == 0) {
      break;
    }

    if (output_len + (size_t)bytes_read + 1 > output_cap) {
      size_t new_cap = output_cap == 0 ? 256 : output_cap;
      char *new_output;
      while (new_cap < output_len + (size_t)bytes_read + 1) {
        if (new_cap > (SIZE_MAX / 2)) {
          free(output);
          output = NULL;
          output_len = 0;
          output_cap = 0;
          close(from_child[0]);
          waitpid(pid, &status, 0);
          return NULL;
        }
        new_cap *= 2;
      }
      new_output = realloc(output, new_cap);
      if (!new_output) {
        free(output);
        output = NULL;
        output_len = 0;
        output_cap = 0;
        close(from_child[0]);
        waitpid(pid, &status, 0);
        return NULL;
      }
      output = new_output;
      output_cap = new_cap;
    }

    memcpy(output + output_len, chunk, (size_t)bytes_read);
    output_len += (size_t)bytes_read;
    output[output_len] = '\0';
  }

  close(from_child[0]);
  waitpid(pid, &status, 0);

  if (!WIFEXITED(status) || WEXITSTATUS(status) != 0) {
    free(output);
    return NULL;
  }

  if (!output || output_len == 0) {
    free(output);
    return NULL;
  }

  {
    char *newline = strchr(output, '\n');
    if (newline) {
      *newline = '\0';
    }
  }

  if (output[0] == '\0') {
    free(output);
    return NULL;
  }

  return output;
}

char *askChoices(const char *choices) {
  return askChoicesWithPrompt(choices, NULL);
}
