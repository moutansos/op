#include <ctype.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

char *strtrim(char *str) {
  size_t len = strlen(str);

  if (len == 0) {
    return str;
  }

  size_t start = 0;

  while (isspace(str[start])) {
    start++;
  }

  size_t end = len - 1;

  while (isspace(str[end])) {
    end--;
  }

  size_t i;

  for (i = 0; i <= end - start; i++) {
    str[i] = str[start + i];
  }

  str[i] = '\0';

  return str;
}

struct OpConfigs {
  char *sourceDir;
  bool isServer;
};

void runSet(char *args, struct OpConfigs *currentConfig) {
  char *key = strtrim(strtok(args, " "));
  char *value = strtrim(strtok(NULL, ""));

  printf("\nSetting %s to '%s'\n", key, value);

  if (strcmp(key, "sourceDir") == 0) {
    currentConfig->sourceDir = value;
    printf("\nSet sourceDir in config to '%s'\n", currentConfig->sourceDir);
  } else if (strcmp(key, "isServer") == 0) {
    currentConfig->isServer = strcmp(value, "true") == 0;
  } else {
    printf("Unknown config key: %s\n", key);
    exit(EXIT_FAILURE);
  }
}

void processLine(char *line, struct OpConfigs *currentConfig) {
  if (line[0] == '#') {
    return;
  } else if (line[0] == '\n') {
    return;
  } else if (line[0] == '\0') {
    return;
  }

  char *command = strtok(line, " ");
  char *args = strtok(NULL, "");

  if (strcmp(command, "set") == 0) {
    runSet(args, currentConfig);
  } else {
    printf("Unknown config command: %s\n", command);
    exit(EXIT_FAILURE);
  }
}

void loadConfig(char *configFile, struct OpConfigs *currentConfig) {
  if (access(configFile, F_OK) != 0) {
    return;
  }

  FILE *fp;
  fp = fopen(configFile, "r");

  if (currentConfig == NULL) {
    currentConfig = malloc(sizeof(struct OpConfigs));
  }

  char line[256];
  while (fgets(line, sizeof(line), fp)) {
    processLine(line, currentConfig);
  }
}

void printConfig(struct OpConfigs *config) {
  printf("Config:\n");
  printf("  sourceDir: %s\n", config->sourceDir);
  printf("  isServer: %s\n", config->isServer ? "true" : "false");
}

struct OpConfigs *loadConfigs() {
  printf("Loading configs...\n");
  struct OpConfigs *config = malloc(sizeof(struct OpConfigs));

  // Set defaults
  config->isServer = false;
  config->sourceDir = "UNSET";

  loadConfig("./op.rc", config);
  printf("\nAt end of loadConfig sourceDir is '%s'\n", config->sourceDir);
  return config;
}
