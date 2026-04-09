package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"standings-edu/internal/studentintake"
)

const (
	rpcMethodStudentSubmit = "student_intake.submit"
)

type jsonRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (h *Handlers) StandingsRPC(w http.ResponseWriter, r *http.Request) {
	if h.intake == nil {
		writeRPCError(w, http.StatusInternalServerError, nil, -32603, "intake store is not configured")
		return
	}

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, -32700, "parse error")
		return
	}

	if req.JSONRPC != "2.0" {
		writeRPCError(w, http.StatusBadRequest, req.ID, -32600, "invalid request: jsonrpc must be \"2.0\"")
		return
	}
	if req.Method != rpcMethodStudentSubmit {
		writeRPCError(w, http.StatusBadRequest, req.ID, -32601, "method not found")
		return
	}

	fields, err := parseStringParams(req.Params)
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, req.ID, -32602, err.Error())
		return
	}

	student, err := h.intake.Submit(fields)
	if err != nil {
		if errors.Is(err, studentintake.ErrMissingFullName) {
			writeRPCError(w, http.StatusBadRequest, req.ID, -32602, "invalid params: full_name is required")
			return
		}
		if errors.Is(err, studentintake.ErrInvalidGroupSlug) {
			writeRPCError(w, http.StatusBadRequest, req.ID, -32602, "invalid params: group must be a valid slug")
			return
		}
		h.logger.Printf("ERROR rpc method=%s err=%v", req.Method, err)
		msg := strings.TrimSpace(err.Error())
		if msg == "" {
			msg = "internal error"
		}
		writeRPCError(w, http.StatusInternalServerError, req.ID, -32603, msg)
		return
	}

	writeRPCResponse(w, http.StatusOK, jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"ok":        true,
			"id":        student.ID,
			"full_name": student.FullName,
		},
	})
}

func parseStringParams(params map[string]any) (map[string]string, error) {
	if params == nil {
		return nil, fmt.Errorf("invalid params: params object is required")
	}

	fields := make(map[string]string, len(params))
	for key, value := range params {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid params: field %q must be string", key)
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(s)
	}

	return fields, nil
}

func writeRPCResponse(w http.ResponseWriter, statusCode int, response jsonRPCResponse) {
	b, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = w.Write(append(b, '\n'))
}

func writeRPCError(w http.ResponseWriter, statusCode int, id any, code int, message string) {
	writeRPCResponse(w, statusCode, jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &jsonRPCError{
			Code:    code,
			Message: message,
		},
	})
}
