#include "fzflib.h"
#include "configlib.h"
#include "pathlib.h"
#include <dirent.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <unistd.h>

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

void execShell() {
    char *shell = getenv("SHELL");
    if (!shell) shell = "/bin/bash";

    execl(shell, shell, NULL);
}

void cdHere(char *path) {
  printf("Changing directory to: %s\n", path);
  if (chdir(path) != 0) {
    perror("Unable to change directory");
    exit(EXIT_FAILURE);
  }

  execShell();
}

void nvimHere(char *path) {
  printf("Opening nvim in directory: %s\n", path);
  char *nvim = "nvim";
  char cmd[1024];
  if(chdir(path) != 0) {
    perror("Unable to change directory for nvim");
    exit(EXIT_FAILURE);
  }
  snprintf(cmd, sizeof(cmd), "%s %s", nvim, path);
  system(cmd);
}

int main() {
  struct OpConfigs* config = loadConfigs();

  // print_hex(config->sourceDir);

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
  } else if (strcmp(actionSelected, "nvim-here") == 0) {
    nvimHere(path);
  } else {
    printf("Unknown action: %s\n", actionSelected);
    exit(EXIT_FAILURE);
  }

  free(path);
  free(selectedValue);
  free(actionSelected);
  free(config);
  free(choices);
  return 0;
}
