#include <stdbool.h>

#ifndef CONFIGLIB_H
#define CONFIGLIB_H

struct OpConfigs {
  char *sourceDir;
  bool isServer;
};

struct OpConfigs *loadConfigs();
void printConfig(struct OpConfigs *config);

#endif // CONFIGLIB_H
