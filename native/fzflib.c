#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <dirent.h>

// Function to run fzf and get the selected value
char* askChoices(const char* choices) {
    char* cmd = "bash -c 'fzf <<< \"%s\"'";
    char cmd_buffer[256];
    char buffer[128];
    char* selectedValue = NULL;

    // Create the full command
    snprintf(cmd_buffer, sizeof(cmd_buffer), cmd, choices);

    FILE* fp = popen(cmd_buffer, "r");

    if (fp == NULL) {
        perror("popen");
        exit(EXIT_FAILURE);
    }

    // Read the selected value from fzf's stdout
    if (fgets(buffer, sizeof(buffer), fp) != NULL) {
        // Remove newline character
        char* newline = strchr(buffer, '\n');
        if (newline != NULL) {
            *newline = '\0';
        }

        selectedValue = strdup(buffer);
    }

    pclose(fp);
    return selectedValue;
}
