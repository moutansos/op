#ifndef CONFIGLIB_H
#define CONFIGLIB_H

#include <stdbool.h>
#include <stddef.h>

typedef struct {
  char *name;
  char *win_path;
  char *linux_path;
} OpCustomEntry;

typedef struct {
  char *name;
  char *command;
  bool run_in_preferred_shell;
} OpCustomCommand;

typedef struct {
  char *config_path;
  char *repo_directory;
  char *wsl_repo_directory;
  bool is_server;
  char *preferred_shell;
  OpCustomEntry *custom_entries;
  size_t custom_entry_count;
  OpCustomCommand *custom_commands;
  size_t custom_command_count;
} OpConfig;

OpConfig *loadConfigs(const char *config_path);
void freeConfig(OpConfig *config);
void printConfig(const OpConfig *config);

#endif
