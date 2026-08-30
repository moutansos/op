#include "configlib.h"

#include <ctype.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
  const char *cur;
  const char *end;
  const char *error;
} JsonParser;

static void free_custom_entry(OpCustomEntry *entry) {
  if (!entry) {
    return;
  }

  free(entry->name);
  free(entry->win_path);
  free(entry->linux_path);
  entry->name = NULL;
  entry->win_path = NULL;
  entry->linux_path = NULL;
}

static void free_custom_command(OpCustomCommand *command) {
  if (!command) {
    return;
  }

  free(command->name);
  free(command->command);
  command->name = NULL;
  command->command = NULL;
}

void freeConfig(OpConfig *config) {
  size_t i = 0;

  if (!config) {
    return;
  }

  free(config->config_path);
  free(config->repo_directory);
  free(config->wsl_repo_directory);
  free(config->preferred_shell);

  for (i = 0; i < config->custom_entry_count; ++i) {
    free_custom_entry(&config->custom_entries[i]);
  }
  free(config->custom_entries);

  for (i = 0; i < config->custom_command_count; ++i) {
    free_custom_command(&config->custom_commands[i]);
  }
  free(config->custom_commands);

  free(config);
}

static void skip_ws(JsonParser *parser) {
  while (parser->cur < parser->end && isspace((unsigned char)*parser->cur)) {
    parser->cur++;
  }
}

static int consume_char(JsonParser *parser, char ch) {
  skip_ws(parser);
  if (parser->cur >= parser->end || *parser->cur != ch) {
    parser->error = "Unexpected character while parsing JSON";
    return 0;
  }
  parser->cur++;
  return 1;
}

static int hex_value(char ch) {
  if (ch >= '0' && ch <= '9') {
    return ch - '0';
  }
  if (ch >= 'a' && ch <= 'f') {
    return 10 + ch - 'a';
  }
  if (ch >= 'A' && ch <= 'F') {
    return 10 + ch - 'A';
  }
  return -1;
}

static int append_char(char **buffer, size_t *len, size_t *cap, char ch) {
  char *new_buffer;

  if (*len + 1 >= *cap) {
    size_t new_cap = (*cap == 0) ? 32 : (*cap * 2);
    if (new_cap < *cap) {
      return 0;
    }

    new_buffer = realloc(*buffer, new_cap);
    if (!new_buffer) {
      return 0;
    }

    *buffer = new_buffer;
    *cap = new_cap;
  }

  (*buffer)[*len] = ch;
  (*len)++;
  (*buffer)[*len] = '\0';
  return 1;
}

static char *parse_json_string(JsonParser *parser) {
  char *output = NULL;
  size_t output_len = 0;
  size_t output_cap = 0;

  if (!consume_char(parser, '"')) {
    return NULL;
  }

  while (parser->cur < parser->end) {
    char ch = *parser->cur;

    if (ch == '"') {
      parser->cur++;
      return output ? output : strdup("");
    }

    if (ch == '\\') {
      parser->cur++;
      if (parser->cur >= parser->end) {
        parser->error = "Invalid JSON escape sequence";
        free(output);
        return NULL;
      }

      ch = *parser->cur;
      switch (ch) {
      case '"':
      case '\\':
      case '/':
        if (!append_char(&output, &output_len, &output_cap, ch)) {
          parser->error = "Out of memory while parsing JSON string";
          free(output);
          return NULL;
        }
        parser->cur++;
        break;
      case 'b':
        if (!append_char(&output, &output_len, &output_cap, '\b')) {
          parser->error = "Out of memory while parsing JSON string";
          free(output);
          return NULL;
        }
        parser->cur++;
        break;
      case 'f':
        if (!append_char(&output, &output_len, &output_cap, '\f')) {
          parser->error = "Out of memory while parsing JSON string";
          free(output);
          return NULL;
        }
        parser->cur++;
        break;
      case 'n':
        if (!append_char(&output, &output_len, &output_cap, '\n')) {
          parser->error = "Out of memory while parsing JSON string";
          free(output);
          return NULL;
        }
        parser->cur++;
        break;
      case 'r':
        if (!append_char(&output, &output_len, &output_cap, '\r')) {
          parser->error = "Out of memory while parsing JSON string";
          free(output);
          return NULL;
        }
        parser->cur++;
        break;
      case 't':
        if (!append_char(&output, &output_len, &output_cap, '\t')) {
          parser->error = "Out of memory while parsing JSON string";
          free(output);
          return NULL;
        }
        parser->cur++;
        break;
      case 'u': {
        int h0;
        int h1;
        int h2;
        int h3;
        unsigned int codepoint;
        parser->cur++;
        if (parser->cur + 4 > parser->end) {
          parser->error = "Invalid JSON unicode escape";
          free(output);
          return NULL;
        }

        h0 = hex_value(parser->cur[0]);
        h1 = hex_value(parser->cur[1]);
        h2 = hex_value(parser->cur[2]);
        h3 = hex_value(parser->cur[3]);
        if (h0 < 0 || h1 < 0 || h2 < 0 || h3 < 0) {
          parser->error = "Invalid JSON unicode escape";
          free(output);
          return NULL;
        }

        codepoint = (unsigned int)((h0 << 12) | (h1 << 8) | (h2 << 4) | h3);
        parser->cur += 4;

        if (codepoint <= 0x7F) {
          if (!append_char(&output, &output_len, &output_cap, (char)codepoint)) {
            parser->error = "Out of memory while parsing JSON string";
            free(output);
            return NULL;
          }
        } else {
          if (!append_char(&output, &output_len, &output_cap, '?')) {
            parser->error = "Out of memory while parsing JSON string";
            free(output);
            return NULL;
          }
        }
      } break;
      default:
        parser->error = "Unsupported JSON escape sequence";
        free(output);
        return NULL;
      }
      continue;
    }

    if ((unsigned char)ch < 0x20) {
      parser->error = "Control character in JSON string";
      free(output);
      return NULL;
    }

    if (!append_char(&output, &output_len, &output_cap, ch)) {
      parser->error = "Out of memory while parsing JSON string";
      free(output);
      return NULL;
    }
    parser->cur++;
  }

  parser->error = "Unterminated JSON string";
  free(output);
  return NULL;
}

static int parse_json_bool(JsonParser *parser, bool *value) {
  skip_ws(parser);

  if ((size_t)(parser->end - parser->cur) >= 4 &&
      strncmp(parser->cur, "true", 4) == 0) {
    parser->cur += 4;
    *value = true;
    return 1;
  }

  if ((size_t)(parser->end - parser->cur) >= 5 &&
      strncmp(parser->cur, "false", 5) == 0) {
    parser->cur += 5;
    *value = false;
    return 1;
  }

  parser->error = "Expected boolean value";
  return 0;
}

static int skip_json_value(JsonParser *parser);

static int skip_json_object(JsonParser *parser) {
  if (!consume_char(parser, '{')) {
    return 0;
  }

  skip_ws(parser);
  if (parser->cur < parser->end && *parser->cur == '}') {
    parser->cur++;
    return 1;
  }

  while (parser->cur < parser->end) {
    char *key = parse_json_string(parser);
    if (!key) {
      return 0;
    }
    free(key);

    if (!consume_char(parser, ':')) {
      return 0;
    }

    if (!skip_json_value(parser)) {
      return 0;
    }

    skip_ws(parser);
    if (parser->cur < parser->end && *parser->cur == ',') {
      parser->cur++;
      continue;
    }

    if (parser->cur < parser->end && *parser->cur == '}') {
      parser->cur++;
      return 1;
    }

    parser->error = "Malformed JSON object";
    return 0;
  }

  parser->error = "Unterminated JSON object";
  return 0;
}

static int skip_json_array(JsonParser *parser) {
  if (!consume_char(parser, '[')) {
    return 0;
  }

  skip_ws(parser);
  if (parser->cur < parser->end && *parser->cur == ']') {
    parser->cur++;
    return 1;
  }

  while (parser->cur < parser->end) {
    if (!skip_json_value(parser)) {
      return 0;
    }

    skip_ws(parser);
    if (parser->cur < parser->end && *parser->cur == ',') {
      parser->cur++;
      continue;
    }

    if (parser->cur < parser->end && *parser->cur == ']') {
      parser->cur++;
      return 1;
    }

    parser->error = "Malformed JSON array";
    return 0;
  }

  parser->error = "Unterminated JSON array";
  return 0;
}

static int skip_json_number(JsonParser *parser) {
  const char *start = parser->cur;

  if (parser->cur < parser->end && *parser->cur == '-') {
    parser->cur++;
  }

  if (parser->cur >= parser->end || !isdigit((unsigned char)*parser->cur)) {
    parser->error = "Malformed JSON number";
    return 0;
  }

  if (*parser->cur == '0') {
    parser->cur++;
  } else {
    while (parser->cur < parser->end && isdigit((unsigned char)*parser->cur)) {
      parser->cur++;
    }
  }

  if (parser->cur < parser->end && *parser->cur == '.') {
    parser->cur++;
    if (parser->cur >= parser->end || !isdigit((unsigned char)*parser->cur)) {
      parser->error = "Malformed JSON number";
      return 0;
    }
    while (parser->cur < parser->end && isdigit((unsigned char)*parser->cur)) {
      parser->cur++;
    }
  }

  if (parser->cur < parser->end && (*parser->cur == 'e' || *parser->cur == 'E')) {
    parser->cur++;
    if (parser->cur < parser->end && (*parser->cur == '+' || *parser->cur == '-')) {
      parser->cur++;
    }
    if (parser->cur >= parser->end || !isdigit((unsigned char)*parser->cur)) {
      parser->error = "Malformed JSON number";
      return 0;
    }
    while (parser->cur < parser->end && isdigit((unsigned char)*parser->cur)) {
      parser->cur++;
    }
  }

  return parser->cur > start;
}

static int skip_json_value(JsonParser *parser) {
  skip_ws(parser);

  if (parser->cur >= parser->end) {
    parser->error = "Unexpected end of JSON input";
    return 0;
  }

  switch (*parser->cur) {
  case '{':
    return skip_json_object(parser);
  case '[':
    return skip_json_array(parser);
  case '"': {
    char *tmp = parse_json_string(parser);
    if (!tmp) {
      return 0;
    }
    free(tmp);
    return 1;
  }
  case 't':
    if ((size_t)(parser->end - parser->cur) >= 4 &&
        strncmp(parser->cur, "true", 4) == 0) {
      parser->cur += 4;
      return 1;
    }
    parser->error = "Malformed JSON value";
    return 0;
  case 'f':
    if ((size_t)(parser->end - parser->cur) >= 5 &&
        strncmp(parser->cur, "false", 5) == 0) {
      parser->cur += 5;
      return 1;
    }
    parser->error = "Malformed JSON value";
    return 0;
  case 'n':
    if ((size_t)(parser->end - parser->cur) >= 4 &&
        strncmp(parser->cur, "null", 4) == 0) {
      parser->cur += 4;
      return 1;
    }
    parser->error = "Malformed JSON value";
    return 0;
  default:
    return skip_json_number(parser);
  }
}

static int parse_paths_object(JsonParser *parser, OpCustomEntry *entry) {
  if (!consume_char(parser, '{')) {
    return 0;
  }

  skip_ws(parser);
  if (parser->cur < parser->end && *parser->cur == '}') {
    parser->cur++;
    return 1;
  }

  while (parser->cur < parser->end) {
    char *key = parse_json_string(parser);
    if (!key) {
      return 0;
    }

    if (!consume_char(parser, ':')) {
      free(key);
      return 0;
    }

    if (strcmp(key, "win") == 0) {
      char *value = parse_json_string(parser);
      if (!value) {
        free(key);
        return 0;
      }
      free(entry->win_path);
      entry->win_path = value;
    } else if (strcmp(key, "linux") == 0) {
      char *value = parse_json_string(parser);
      if (!value) {
        free(key);
        return 0;
      }
      free(entry->linux_path);
      entry->linux_path = value;
    } else {
      if (!skip_json_value(parser)) {
        free(key);
        return 0;
      }
    }

    free(key);

    skip_ws(parser);
    if (parser->cur < parser->end && *parser->cur == ',') {
      parser->cur++;
      continue;
    }

    if (parser->cur < parser->end && *parser->cur == '}') {
      parser->cur++;
      return 1;
    }

    parser->error = "Malformed custom entry paths object";
    return 0;
  }

  parser->error = "Unterminated custom entry paths object";
  return 0;
}

static int parse_custom_entry(JsonParser *parser, OpCustomEntry *entry) {
  memset(entry, 0, sizeof(*entry));

  if (!consume_char(parser, '{')) {
    return 0;
  }

  skip_ws(parser);
  if (parser->cur < parser->end && *parser->cur == '}') {
    parser->cur++;
    return 1;
  }

  while (parser->cur < parser->end) {
    char *key = parse_json_string(parser);
    if (!key) {
      return 0;
    }

    if (!consume_char(parser, ':')) {
      free(key);
      return 0;
    }

    if (strcmp(key, "name") == 0) {
      char *value = parse_json_string(parser);
      if (!value) {
        free(key);
        return 0;
      }
      free(entry->name);
      entry->name = value;
    } else if (strcmp(key, "paths") == 0) {
      if (!parse_paths_object(parser, entry)) {
        free(key);
        return 0;
      }
    } else {
      if (!skip_json_value(parser)) {
        free(key);
        return 0;
      }
    }

    free(key);

    skip_ws(parser);
    if (parser->cur < parser->end && *parser->cur == ',') {
      parser->cur++;
      continue;
    }

    if (parser->cur < parser->end && *parser->cur == '}') {
      parser->cur++;
      return 1;
    }

    parser->error = "Malformed custom entry object";
    return 0;
  }

  parser->error = "Unterminated custom entry object";
  return 0;
}

static int append_custom_entry(OpConfig *config, OpCustomEntry *entry) {
  OpCustomEntry *new_entries;
  size_t new_count = config->custom_entry_count + 1;

  new_entries = realloc(config->custom_entries, new_count * sizeof(*new_entries));
  if (!new_entries) {
    return 0;
  }

  config->custom_entries = new_entries;
  config->custom_entries[config->custom_entry_count] = *entry;
  config->custom_entry_count = new_count;
  return 1;
}

static int parse_custom_entries_array(JsonParser *parser, OpConfig *config) {
  if (!consume_char(parser, '[')) {
    return 0;
  }

  skip_ws(parser);
  if (parser->cur < parser->end && *parser->cur == ']') {
    parser->cur++;
    return 1;
  }

  while (parser->cur < parser->end) {
    OpCustomEntry entry;
    memset(&entry, 0, sizeof(entry));

    if (!parse_custom_entry(parser, &entry)) {
      free_custom_entry(&entry);
      return 0;
    }

    if (!append_custom_entry(config, &entry)) {
      parser->error = "Out of memory while parsing custom entries";
      free_custom_entry(&entry);
      return 0;
    }

    skip_ws(parser);
    if (parser->cur < parser->end && *parser->cur == ',') {
      parser->cur++;
      continue;
    }

    if (parser->cur < parser->end && *parser->cur == ']') {
      parser->cur++;
      return 1;
    }

    parser->error = "Malformed custom entries array";
    return 0;
  }

  parser->error = "Unterminated custom entries array";
  return 0;
}

static int parse_custom_command(JsonParser *parser, OpCustomCommand *command) {
  memset(command, 0, sizeof(*command));

  if (!consume_char(parser, '{')) {
    return 0;
  }

  skip_ws(parser);
  if (parser->cur < parser->end && *parser->cur == '}') {
    parser->cur++;
    return 1;
  }

  while (parser->cur < parser->end) {
    char *key = parse_json_string(parser);
    if (!key) {
      return 0;
    }

    if (!consume_char(parser, ':')) {
      free(key);
      return 0;
    }

    if (strcmp(key, "name") == 0) {
      char *value = parse_json_string(parser);
      if (!value) {
        free(key);
        return 0;
      }
      free(command->name);
      command->name = value;
    } else if (strcmp(key, "command") == 0) {
      char *value = parse_json_string(parser);
      if (!value) {
        free(key);
        return 0;
      }
      free(command->command);
      command->command = value;
    } else if (strcmp(key, "runInPreferredShell") == 0) {
      if (!parse_json_bool(parser, &command->run_in_preferred_shell)) {
        free(key);
        return 0;
      }
    } else {
      if (!skip_json_value(parser)) {
        free(key);
        return 0;
      }
    }

    free(key);

    skip_ws(parser);
    if (parser->cur < parser->end && *parser->cur == ',') {
      parser->cur++;
      continue;
    }

    if (parser->cur < parser->end && *parser->cur == '}') {
      parser->cur++;
      return 1;
    }

    parser->error = "Malformed custom command object";
    return 0;
  }

  parser->error = "Unterminated custom command object";
  return 0;
}

static int append_custom_command(OpConfig *config, OpCustomCommand *command) {
  OpCustomCommand *new_commands;
  size_t new_count = config->custom_command_count + 1;

  new_commands = realloc(config->custom_commands, new_count * sizeof(*new_commands));
  if (!new_commands) {
    return 0;
  }

  config->custom_commands = new_commands;
  config->custom_commands[config->custom_command_count] = *command;
  config->custom_command_count = new_count;
  return 1;
}

static int parse_custom_commands_array(JsonParser *parser, OpConfig *config) {
  if (!consume_char(parser, '[')) {
    return 0;
  }

  skip_ws(parser);
  if (parser->cur < parser->end && *parser->cur == ']') {
    parser->cur++;
    return 1;
  }

  while (parser->cur < parser->end) {
    OpCustomCommand command;
    memset(&command, 0, sizeof(command));

    if (!parse_custom_command(parser, &command)) {
      free_custom_command(&command);
      return 0;
    }

    if (!append_custom_command(config, &command)) {
      parser->error = "Out of memory while parsing custom commands";
      free_custom_command(&command);
      return 0;
    }

    skip_ws(parser);
    if (parser->cur < parser->end && *parser->cur == ',') {
      parser->cur++;
      continue;
    }

    if (parser->cur < parser->end && *parser->cur == ']') {
      parser->cur++;
      return 1;
    }

    parser->error = "Malformed custom commands array";
    return 0;
  }

  parser->error = "Unterminated custom commands array";
  return 0;
}

static int parse_config_object(JsonParser *parser, OpConfig *config) {
  if (!consume_char(parser, '{')) {
    return 0;
  }

  skip_ws(parser);
  if (parser->cur < parser->end && *parser->cur == '}') {
    parser->cur++;
    return 1;
  }

  while (parser->cur < parser->end) {
    char *key = parse_json_string(parser);
    if (!key) {
      return 0;
    }

    if (!consume_char(parser, ':')) {
      free(key);
      return 0;
    }

    if (strcmp(key, "repoDirectory") == 0) {
      char *value = parse_json_string(parser);
      if (!value) {
        free(key);
        return 0;
      }
      free(config->repo_directory);
      config->repo_directory = value;
    } else if (strcmp(key, "wslRepoDirectory") == 0) {
      char *value = parse_json_string(parser);
      if (!value) {
        free(key);
        return 0;
      }
      free(config->wsl_repo_directory);
      config->wsl_repo_directory = value;
    } else if (strcmp(key, "isServer") == 0) {
      if (!parse_json_bool(parser, &config->is_server)) {
        free(key);
        return 0;
      }
    } else if (strcmp(key, "preferedShell") == 0) {
      char *value = parse_json_string(parser);
      if (!value) {
        free(key);
        return 0;
      }
      free(config->preferred_shell);
      config->preferred_shell = value;
    } else if (strcmp(key, "customEntries") == 0) {
      if (!parse_custom_entries_array(parser, config)) {
        free(key);
        return 0;
      }
    } else if (strcmp(key, "customCommands") == 0) {
      if (!parse_custom_commands_array(parser, config)) {
        free(key);
        return 0;
      }
    } else {
      if (!skip_json_value(parser)) {
        free(key);
        return 0;
      }
    }

    free(key);

    skip_ws(parser);
    if (parser->cur < parser->end && *parser->cur == ',') {
      parser->cur++;
      continue;
    }

    if (parser->cur < parser->end && *parser->cur == '}') {
      parser->cur++;
      return 1;
    }

    parser->error = "Malformed config JSON object";
    return 0;
  }

  parser->error = "Unterminated config JSON object";
  return 0;
}

static char *read_file_to_buffer(const char *path, size_t *out_len) {
  FILE *fp;
  long size_long;
  size_t size;
  size_t bytes_read;
  char *buffer;

  *out_len = 0;
  fp = fopen(path, "rb");
  if (!fp) {
    return NULL;
  }

  if (fseek(fp, 0L, SEEK_END) != 0) {
    fclose(fp);
    return NULL;
  }

  size_long = ftell(fp);
  if (size_long < 0) {
    fclose(fp);
    return NULL;
  }

  if (fseek(fp, 0L, SEEK_SET) != 0) {
    fclose(fp);
    return NULL;
  }

  size = (size_t)size_long;
  buffer = malloc(size + 1);
  if (!buffer) {
    fclose(fp);
    return NULL;
  }

  bytes_read = fread(buffer, 1, size, fp);
  fclose(fp);

  if (bytes_read != size) {
    free(buffer);
    return NULL;
  }

  buffer[size] = '\0';
  *out_len = size;
  return buffer;
}

OpConfig *loadConfigs(const char *config_path) {
  OpConfig *config;
  JsonParser parser;
  char *json_buffer = NULL;
  size_t json_length = 0;

  if (!config_path) {
    fprintf(stderr, "Config path is required\n");
    return NULL;
  }

  json_buffer = read_file_to_buffer(config_path, &json_length);
  if (!json_buffer) {
    fprintf(stderr, "Failed to read config file '%s': %s\n", config_path,
            strerror(errno));
    return NULL;
  }

  config = calloc(1, sizeof(*config));
  if (!config) {
    free(json_buffer);
    return NULL;
  }

  config->config_path = strdup(config_path);
  config->repo_directory = strdup("~/source/repos");
  config->wsl_repo_directory = strdup("/mnt/c/source/repos");
  config->preferred_shell = strdup("bash");
  config->is_server = false;

  if (!config->config_path || !config->repo_directory || !config->wsl_repo_directory ||
      !config->preferred_shell) {
    free(json_buffer);
    freeConfig(config);
    return NULL;
  }

  parser.cur = json_buffer;
  parser.end = json_buffer + json_length;
  parser.error = NULL;

  if (!parse_config_object(&parser, config)) {
    fprintf(stderr, "Failed to parse config file '%s': %s\n", config_path,
            parser.error ? parser.error : "Unknown error");
    free(json_buffer);
    freeConfig(config);
    return NULL;
  }

  skip_ws(&parser);
  if (parser.cur != parser.end) {
    fprintf(stderr,
            "Failed to parse config file '%s': trailing characters found\n",
            config_path);
    free(json_buffer);
    freeConfig(config);
    return NULL;
  }

  free(json_buffer);
  return config;
}

void printConfig(const OpConfig *config) {
  size_t i = 0;

  if (!config) {
    printf("Config: (null)\n");
    return;
  }

  printf("Config:\n");
  printf("  configPath: %s\n", config->config_path ? config->config_path : "");
  printf("  repoDirectory: %s\n",
         config->repo_directory ? config->repo_directory : "");
  printf("  wslRepoDirectory: %s\n",
         config->wsl_repo_directory ? config->wsl_repo_directory : "");
  printf("  isServer: %s\n", config->is_server ? "true" : "false");
  printf("  preferedShell: %s\n",
         config->preferred_shell ? config->preferred_shell : "");

  printf("  customEntries: %zu\n", config->custom_entry_count);
  for (i = 0; i < config->custom_entry_count; ++i) {
    printf("    - %s\n",
           config->custom_entries[i].name ? config->custom_entries[i].name : "");
  }

  printf("  customCommands: %zu\n", config->custom_command_count);
  for (i = 0; i < config->custom_command_count; ++i) {
    printf("    - %s\n",
           config->custom_commands[i].name ? config->custom_commands[i].name : "");
  }
}
