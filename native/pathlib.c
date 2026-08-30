#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/types.h>
#include <pwd.h>

char* expand_tilde(const char* path) {
    if (path[0] != '~') {
        // No tilde, return copy of original path
        return strdup(path);
    }
    
    const char* home_dir;
    const char* rest_of_path;
    
    if (path[1] == '/' || path[1] == '\0') {
        // ~/... or just ~
        home_dir = getenv("HOME");
        if (!home_dir) {
            // Fallback to passwd entry
            struct passwd* pw = getpwuid(getuid());
            if (pw) {
                home_dir = pw->pw_dir;
            } else {
                return NULL; // Cannot resolve
            }
        }
        rest_of_path = path + 1; // Skip the ~
    } else {
        // ~username/... format
        const char* slash = strchr(path + 1, '/');
        size_t username_len;
        
        if (slash) {
            username_len = slash - path - 1;
            rest_of_path = slash;
        } else {
            username_len = strlen(path + 1);
            rest_of_path = "";
        }
        
        // Extract username
        char* username = malloc(username_len + 1);
        if (!username) {
            return NULL;
        }
        strncpy(username, path + 1, username_len);
        username[username_len] = '\0';
        
        // Look up user
        struct passwd* pw = getpwnam(username);
        free(username);
        
        if (pw) {
            home_dir = pw->pw_dir;
        } else {
            return NULL; // User not found
        }
    }
    
    // Build result
    size_t home_len = strlen(home_dir);
    size_t rest_len = strlen(rest_of_path);
    
    char* result = malloc(home_len + rest_len + 1);
    if (!result) {
        return NULL;
    }
    
    strcpy(result, home_dir);
    strcat(result, rest_of_path);
    
    return result; // Caller must free()
}

// Convert relative paths to absolute (without resolving symlinks)
char* make_absolute_path(const char* path) {
    // First expand ~ if present
    char* expanded = expand_tilde(path);
    if (!expanded) {
        return NULL;
    }
    
    // If already absolute, return as-is
    if (expanded[0] == '/') {
        return expanded;
    }
    
    // Get current directory and prepend to relative path
    char* cwd = getcwd(NULL, 0);
    if (!cwd) {
        free(expanded);
        return NULL;
    }
    
    size_t cwd_len = strlen(cwd);
    size_t path_len = strlen(expanded);
    char* absolute = malloc(cwd_len + 1 + path_len + 1);
    
    if (absolute) {
        strcpy(absolute, cwd);
        strcat(absolute, "/");
        strcat(absolute, expanded);
    }
    
    free(cwd);
    free(expanded);
    return absolute;
}
