#include "fzflib.h"
#include "configlib.h"
#include "pathlib.h"
#include <dirent.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <unistd.h>
#include <stdbool.h>

#define MAIN_TMUX_SESSION_NAME "code2"

char *getDirectoryListing(char *directory) {
  DIR *dirp = opendir(directory);
  char *buffer = malloc(1024);
  int errno = 0;
  struct dirent *dp;
  while (dirp) {
    errno = 0;
    if ((dp = readdir(dirp)) != NULL) {
      if (strcmp(dp->d_name, ".") == 0 || strcmp(dp->d_name, "..") == 0) {
        continue;
      }

      if (strlen(buffer) + strlen(dp->d_name) + 1 > 1024) {
        buffer = realloc(buffer, (strlen(buffer) + strlen(dp->d_name) + 1) * 2);
      }

      strcat(buffer, dp->d_name);
      strcat(buffer, "\n");
    } else {
      if (errno == 0) {
        break;
      } else {
        perror("readdir");
        exit(EXIT_FAILURE);
      }
    }
  }

  closedir(dirp);
  return buffer;
}

void print_hex(const char *s) {
  while(*s)
    printf("%02x ", (unsigned int) *s++);
  printf("\n");
}

void execShell(char *shellPrefix) {
    char *shell = getenv("SHELL");
    if (!shell) shell = "/bin/bash";

    if (shellPrefix && strlen(shellPrefix) > 0) {
        execl(shell, shell, "-c", shellPrefix, NULL);
    } else {
        execl(shell, shell, NULL);
    }
}

void cdHere(char *path) {
  printf("Changing directory to: %s\n", path);
  if (chdir(path) != 0) {
    perror("Unable to change directory");
    exit(EXIT_FAILURE);
  }
}

char* getCurrentExecutablePath() {
  char *path = malloc(1024);
  ssize_t len = readlink("/proc/self/exe", path, 1024);
  if (len == -1) {
    perror("readlink");
    free(path);
    exit(EXIT_FAILURE);
  }
  path[len] = '\0';
  return path;
}

char* getCurrentExecutableDir() {
  char *path = getCurrentExecutablePath();
  char *lastSlash = strrchr(path, '/');
  if (lastSlash) {
    *lastSlash = '\0'; // Terminate the string at the last slash
  }
  return path;
}

void nvimHere(char *path) {
  printf("Opening nvim in directory: %s\n", path);
  char *nvim = "nvim";
  char cmd[1024];
  snprintf(cmd, sizeof(cmd), "%s %s", nvim, path);
  system(cmd);
}

bool sessionExists(char *sessionName) {
  char cmd[256];
  snprintf(cmd, sizeof(cmd), "tmux has-session -t %s 2>/dev/null", sessionName);
  if (system(cmd) == 0) {
    printf("Session %s already exists.\n", sessionName);
    return true; // Session exists
  }

  return false; // Session does not exist
}

void createTmuxSession(char *sessionName, char *path, char *executablePath) {
  if(sessionExists(sessionName)) {
      return;
  }

  char cmd[512];
  snprintf(cmd, sizeof(cmd), "tmux new-session -d -s %s -c %s -n \"op\"", sessionName, path);
  system(cmd);

  char opCommand[1024];
  snprintf(opCommand, sizeof(opCommand), "tmux send-keys -t %s:%d \"%s -Continuous\" C-m", sessionName, 0, executablePath);
  system(opCommand);
}

void sendKeysToTmuxWindow(char *sessionName, int windowIndex, char *keys) {
  char cmd[512];
  snprintf(cmd, sizeof(cmd), "tmux send-keys -t %s:%d \"%s\" C-m", sessionName, windowIndex, keys);
  system(cmd);
}

void splitTmuxWindowVertically(char *sessionName, int windowIndex) {
  char cmd[256];
  snprintf(cmd, sizeof(cmd), "tmux split-window -t %s:%d", sessionName, windowIndex);
  system(cmd);
}

void splitTmuxWindowHorizontally(char *sessionName, int windowIndex) {
  char cmd[256];
  snprintf(cmd, sizeof(cmd), "tmux split-window -h -t %s:%d", sessionName, windowIndex);
  system(cmd);
}

int createTmuxWindowInSession(char *sessionName, char *windowName) {
  if(!sessionExists(sessionName)) {
      return -1; // Session does not exist
  }

  char cmd[512];
  snprintf(cmd, sizeof(cmd), "tmux new-window -t %s -n \"%s\"", sessionName, windowName);
  system(cmd);

  int windowIndex = 0; // Default to first window
  char listCmd[256];
  snprintf(listCmd, sizeof(listCmd), "tmux list-windows -t %s -F '#{window_index}' | tail -n 1", sessionName);
  FILE *fp = popen(listCmd, "r");
  if (fp != NULL) {
      char buffer[16];
      if (fgets(buffer, sizeof(buffer), fp) != NULL) {
          windowIndex = atoi(buffer);
      }
      pclose(fp);
  } else {
      perror("popen");
      exit(EXIT_FAILURE);
  }
  return windowIndex;
}

void focustTmuxWindowPane(char *sessionName, int windowIndex, int paneIndex) {
  char cmd[256];
  snprintf(cmd, sizeof(cmd), "tmux select-pane -t %s:%d.%d", sessionName, windowIndex, paneIndex);
  system(cmd);
}

void updateGitRepo(char *path) {
  // Check if the path is a git repository by looking for a .git directory
    char gitDir[1024];
    snprintf(gitDir, sizeof(gitDir), "%s/.git", path);
    if (access(gitDir, F_OK) == 0) {
        // It's a git repository, check to see if we can update it by running git status and looking for "Nothing to commit"
        printf("Found git repository at %s\n", path);
        char statusCmd[256];
        snprintf(statusCmd, sizeof(statusCmd), "git -C %s status --porcelain", path);
        printf("Running command: %s\n", statusCmd);
        FILE *fp = popen(statusCmd, "r");
        if (fp == NULL) {
            perror("Failed to run git status");
            exit(EXIT_FAILURE);
        }
        char statusOutput[256];
        if (fgets(statusOutput, sizeof(statusOutput), fp) != NULL) {
            // If the output is empty, there are no changes to commit
            if (statusOutput[0] != '\0') {
                printf("Changes detected in git repository at %s:\nNot pulling updates.", path);
                printf("Status output: %s\n", statusOutput);
                printf("Text output: \n\"%s\"\n", statusOutput);
                pclose(fp);
                return;
            }
        }

        printf("No changes to commit in git repository at %s\n", path);
        printf("Pulling down changes.\n");

        char gitPullCmd[256];
        snprintf(gitPullCmd, sizeof(gitPullCmd), "git -C %s pull", path);
        int status = system(gitPullCmd);
        if (status == -1) {
            perror("Failed to run git pull");
            exit(EXIT_FAILURE);
        } else {
            printf("Git repository updated successfully.\n");
        }
    } else {
        // Not a git repository, do nothing
        printf("No git repository found at %s\n", path);
    }
}

int main() {
  char *executablePath = getCurrentExecutablePath();
  struct OpConfigs* config = loadConfigs();

  printConfig(config);
  char *choices = getDirectoryListing("/home/ben/source/repos");
  char *selectedValue = askChoices(choices);

  if (selectedValue != NULL) {
    printf("Selected value: %s\n", selectedValue);
  } else {
    printf("No value selected.\n");
    exit(EXIT_FAILURE);
  }

  char *actionChoices = {
    "nvim-tmux\n"
    "nvim-here\n"
    "cd-here\n"
  };

  char *actionSelected = askChoices(actionChoices);

  if (actionSelected != NULL) {
    printf("Action selected: %s\n", actionSelected);
  } else {
    printf("No action selected.\n");
    exit(EXIT_FAILURE);
  }

  char *path = malloc(strlen(config->sourceDir) + strlen(selectedValue) + 2);
  sprintf(path, "%s/%s", config->sourceDir, selectedValue);
  path = expand_tilde(path);

  if (strcmp(actionSelected, "cd-here") == 0) {
    cdHere(path);
    execShell(config->shellPrefix);
  } else if (strcmp(actionSelected, "nvim-here") == 0) {
    cdHere(path);
    nvimHere(path);
  } else if (strcmp(actionSelected, "nvim-tmux") == 0) {
    char *sourcePath = make_absolute_path(config->sourceDir);
    char *windowPath = make_absolute_path(path);

    updateGitRepo(windowPath);

    cdHere(sourcePath);
    createTmuxSession(MAIN_TMUX_SESSION_NAME, sourcePath, executablePath);
    int windowIndex = createTmuxWindowInSession(MAIN_TMUX_SESSION_NAME, selectedValue);
    if (windowIndex < 0) {
      printf("Failed to create tmux window in session %s\n", MAIN_TMUX_SESSION_NAME);
      exit(EXIT_FAILURE);
    }
    char fullCdWindowPath[1024];
    snprintf(fullCdWindowPath, sizeof(fullCdWindowPath), "cd %s", windowPath);
    sendKeysToTmuxWindow(MAIN_TMUX_SESSION_NAME, windowIndex, fullCdWindowPath);
    sendKeysToTmuxWindow(MAIN_TMUX_SESSION_NAME, windowIndex, "nvim .");
    splitTmuxWindowVertically(MAIN_TMUX_SESSION_NAME, windowIndex);

    //Setup Terminal Below
    sendKeysToTmuxWindow(MAIN_TMUX_SESSION_NAME, windowIndex, fullCdWindowPath);
    if (strlen(config->shellPrefix) > 0) {
      sendKeysToTmuxWindow(MAIN_TMUX_SESSION_NAME, windowIndex, config->shellPrefix);
    }
    sendKeysToTmuxWindow(MAIN_TMUX_SESSION_NAME, windowIndex, "clear");

    // Select the first pane above the terminal
    focustTmuxWindowPane(MAIN_TMUX_SESSION_NAME, windowIndex, 0);

    // Clean up
    free(sourcePath);
    free(windowPath);
  } else {
    printf("Unknown action: %s\n", actionSelected);
    exit(EXIT_FAILURE);
  }

  // Clean up
  free(executablePath);
  free(path);
  free(selectedValue);
  free(actionSelected);
  free(config);
  free(choices);

  return 0;
}
