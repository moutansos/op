package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/moutansos/op/internal/domain"
)

// ErrorResponse is the common non-success response envelope.
type ErrorResponse struct {
	Error *domain.Error `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	writeStatusError(w, statusForError(err), publicError(err))
}

func writeStatusError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, ErrorResponse{Error: publicError(err)})
}

func statusForError(err error) int {
	switch domain.CodeOf(err) {
	case domain.ErrorCodeInvalidArgument:
		return http.StatusBadRequest
	case domain.ErrorCodeNotFound:
		return http.StatusNotFound
	case domain.ErrorCodeAlreadyExists, domain.ErrorCodeConflict:
		return http.StatusConflict
	case domain.ErrorCodeUnauthorized:
		return http.StatusUnauthorized
	case domain.ErrorCodeForbidden:
		return http.StatusForbidden
	case domain.ErrorCodeDependency:
		return http.StatusServiceUnavailable
	case domain.ErrorCodeCanceled:
		return http.StatusRequestTimeout
	case domain.ErrorCodeTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

var (
	urlUserInfoPattern         = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`)
	bearerPattern              = regexp.MustCompile(`(?i)(bearer\s+)([^\s,;]+)`)
	authorizationBearerPattern = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s,;]+`)
	basicPattern               = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*basic\s+)[^\s,;]+`)
	sensitiveKeyPattern        = regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|passwd|authorization)\s*[:=]\s*)[^\s,;&]+`)
	sensitiveURLPattern        = regexp.MustCompile(`(?i)([?&](?:access_token|token|api_key|password)=)[^&#\s]+`)
)

func publicError(err error) *domain.Error {
	if err == nil {
		return nil
	}
	var typed *domain.Error
	if errors.As(err, &typed) {
		result := &domain.Error{
			Code:     typed.Code,
			Op:       redact(typed.Op),
			Field:    redact(typed.Field),
			Resource: redact(typed.Resource),
			Message:  redact(typed.Message),
		}
		if result.Message == "" {
			result.Message = defaultErrorMessage(result.Code)
		}
		return result
	}
	code := domain.CodeOf(err)
	return domain.NewError(code, "server.request", defaultErrorMessage(code), nil)
}

func defaultErrorMessage(code domain.ErrorCode) string {
	switch code {
	case domain.ErrorCodeCanceled:
		return "request canceled"
	case domain.ErrorCodeTimeout:
		return "request timed out"
	default:
		return "internal server error"
	}
}

func redact(value string) string {
	value = urlUserInfoPattern.ReplaceAllString(value, `${1}[REDACTED]@`)
	value = authorizationBearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = bearerPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := bearerPattern.FindStringSubmatch(match)
		if len(parts) != 3 || isBearerDescriptor(parts[2]) {
			return match
		}
		return parts[1] + "[REDACTED]"
	})
	value = basicPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = sensitiveKeyPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = sensitiveURLPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return strings.TrimSpace(value)
}

func isBearerDescriptor(value string) bool {
	switch strings.ToLower(strings.TrimRight(value, ".:!?")) {
	case "auth", "authentication", "credential", "credentials", "header", "scheme", "token":
		return true
	default:
		return false
	}
}
