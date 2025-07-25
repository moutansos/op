#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <dirent.h>

// Function to run fzf and get the selected value
char* askChoices(const char* choices) {
    char buffer[128];
    char* selectedValue = NULL;
    char cmd[512];
    
    snprintf(cmd, sizeof(cmd), "echo '%s' | fzf", choices);
    FILE* fp = popen(cmd, "r");

    if (fp == NULL) {
        perror("popen");
        exit(EXIT_FAILURE);
    }
    
    if (fgets(buffer, sizeof(buffer), fp) != NULL) {
        char* newline = strchr(buffer, '\n');
        if (newline != NULL) {
            *newline = '\0';
        }
        selectedValue = strdup(buffer);
    }

    pclose(fp);
    return selectedValue;
}
