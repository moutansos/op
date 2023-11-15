#include "fzflib.h"
#include "configlib.h"
#include <dirent.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>

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


int main() {
  struct OpConfigs* config = loadConfigs();
  printf("Source dir: %s\n", config->sourceDir);
  printConfig(config);
  char *choices = getDirectoryListing("/home/ben/source/repos");
  char *selectedValue = askChoices(choices);

  if (selectedValue != NULL) {
    printf("Selected value: %s\n", selectedValue);
    free(selectedValue);
  } else {
    printf("No value selected.\n");
    exit(EXIT_FAILURE);
  }

  free(config);
  free(choices);
  return 0;
}
