package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
)

const maxAdminJSONBodyBytes = 8 << 20

const (
	adminIntakePath        = "data/student_intake.json"
	adminIntakeStagingPath = "data/student_intake_admin.json"
)

type AdminConfig struct {
	Login        string
	Password     string
	ProjectRoot  string
	DataDir      string
	GeneratedDir string
}

type adminState struct {
	cfg      AdminConfig
	actionMu sync.Mutex

	resultMu   sync.RWMutex
	lastResult *AdminActionResult
}

type AdminActionResult struct {
	Action    string   `json:"action"`
	Success   bool     `json:"success"`
	ExitCode  int      `json:"exit_code"`
	StartedAt string   `json:"started_at"`
	Duration  string   `json:"duration"`
	Output    string   `json:"output"`
	Errors    []string `json:"errors,omitempty"`
}

type AdminPageData struct {
	PageTitle   string
	Footer      FooterInfo
	Editable    []string
	LastResult  *AdminActionResult
	DefaultPath string
}

type adminFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type adminIntakeMergeRequest struct {
	Content string `json:"content"`
}

type adminCommand struct {
	Path string
	Args []string
}

func (h *Handlers) ConfigureAdmin(cfg AdminConfig) error {
	cfg.Login = strings.TrimSpace(cfg.Login)
	cfg.Password = strings.TrimSpace(cfg.Password)
	cfg.ProjectRoot = strings.TrimSpace(cfg.ProjectRoot)
	cfg.DataDir = strings.TrimSpace(cfg.DataDir)
	cfg.GeneratedDir = strings.TrimSpace(cfg.GeneratedDir)

	if cfg.Login == "" || cfg.Password == "" {
		return fmt.Errorf("admin credentials are required")
	}
	if cfg.ProjectRoot == "" {
		return fmt.Errorf("project root is required")
	}

	projectRoot, err := filepath.Abs(cfg.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	cfg.ProjectRoot = projectRoot

	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(cfg.ProjectRoot, "data")
	} else if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(cfg.ProjectRoot, cfg.DataDir)
	}
	cfg.DataDir = filepath.Clean(cfg.DataDir)

	if cfg.GeneratedDir == "" {
		cfg.GeneratedDir = filepath.Join(cfg.ProjectRoot, "generated")
	} else if !filepath.IsAbs(cfg.GeneratedDir) {
		cfg.GeneratedDir = filepath.Join(cfg.ProjectRoot, cfg.GeneratedDir)
	}
	cfg.GeneratedDir = filepath.Clean(cfg.GeneratedDir)

	h.admin = &adminState{cfg: cfg}
	return nil
}

func (h *Handlers) AdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.admin == nil {
			http.Error(w, "admin is not configured", http.StatusInternalServerError)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != h.admin.cfg.Login || pass != h.admin.cfg.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (h *Handlers) AdminPage(w http.ResponseWriter, _ *http.Request) {
	files, err := h.listEditableFiles()
	if err != nil {
		h.logger.Printf("ERROR list editable files: %v", err)
		files = []string{"data/students.json", "data/contests.json", adminIntakePath}
	}

	defaultPath := ""
	if len(files) > 0 {
		defaultPath = files[0]
	}

	page := AdminPageData{
		PageTitle:   "Admin",
		Footer:      h.buildFooterInfo(),
		Editable:    files,
		LastResult:  h.lastAdminResult(),
		DefaultPath: defaultPath,
	}
	if err := h.renderer.Render(w, http.StatusOK, "admin.html", page); err != nil {
		h.logger.Printf("ERROR render admin page: %v", err)
	}
}

func (h *Handlers) AdminActionUpdate(w http.ResponseWriter, r *http.Request) {
	result := h.runAdminAction("update/build", func() AdminActionResult {
		return h.executeUpdateAction()
	})
	h.setAdminResult(result)
	http.Redirect(w, r, "/standings/admin", http.StatusSeeOther)
}

func (h *Handlers) AdminActionGenerate(w http.ResponseWriter, r *http.Request) {
	result := h.runAdminAction("generate", func() AdminActionResult {
		return h.executeGenerateAction()
	})
	h.setAdminResult(result)
	http.Redirect(w, r, "/standings/admin", http.StatusSeeOther)
}

func (h *Handlers) AdminGroupCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		result := newAdminResult(
			"create_group",
			false,
			-1,
			time.Now(),
			"",
			[]string{fmt.Sprintf("parse form: %v", err)},
		)
		h.setAdminResult(result)
		http.Redirect(w, r, "/standings/admin", http.StatusSeeOther)
		return
	}

	slug := strings.TrimSpace(r.FormValue("slug"))
	name := strings.TrimSpace(r.FormValue("name"))
	formLink := strings.TrimSpace(r.FormValue("form_link"))

	result := h.runAdminAction("create_group", func() AdminActionResult {
		return h.executeCreateGroupAction(slug, name, formLink)
	})
	h.setAdminResult(result)
	http.Redirect(w, r, "/standings/admin", http.StatusSeeOther)
}

func (h *Handlers) AdminFiles(w http.ResponseWriter, _ *http.Request) {
	files, err := h.listEditableFiles()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"files": files,
	})
}

func (h *Handlers) AdminFile(w http.ResponseWriter, r *http.Request) {
	logicalPath := strings.TrimSpace(r.URL.Query().Get("path"))
	normalizedPath, absPath, err := h.resolveEditablePath(logicalPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	body, err := os.ReadFile(absPath)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path":    normalizedPath,
		"content": string(body),
	})
}

func (h *Handlers) AdminFileValidate(w http.ResponseWriter, r *http.Request) {
	req, err := decodeAdminFileRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	if _, _, err := h.resolveEditablePath(req.Path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	if err := validateJSONSyntax(req.Content); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path":    req.Path,
		"message": "JSON is valid",
	})
}

func (h *Handlers) AdminFileSave(w http.ResponseWriter, r *http.Request) {
	req, err := decodeAdminFileRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	normalizedPath, absPath, err := h.resolveEditablePath(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	if err := validateJSONSyntax(req.Content); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(absPath); statErr == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": statErr.Error(),
		})
		return
	}

	if err := writeFileAtomically(absPath, []byte(req.Content), mode); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path":    normalizedPath,
		"message": "saved",
	})
}

func (h *Handlers) AdminIntakeStagingPrepare(w http.ResponseWriter, _ *http.Request) {
	if h.intake == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": "intake store is not configured",
		})
		return
	}

	stagingPath := filepath.Join(h.admin.cfg.DataDir, "student_intake_admin.json")
	body, err := h.intake.PrepareAdminIntakeStaging(stagingPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path":    adminIntakeStagingPath,
		"content": string(body),
	})
}

func (h *Handlers) AdminIntakeStagingMerge(w http.ResponseWriter, r *http.Request) {
	if h.intake == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": "intake store is not configured",
		})
		return
	}

	req, err := decodeAdminIntakeMergeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	if err := validateJSONSyntax(req.Content); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	stagingPath := filepath.Join(h.admin.cfg.DataDir, "student_intake_admin.json")
	if err := h.intake.SaveAdminIntakeStaging(stagingPath, []byte(req.Content)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	result := h.runAdminAction("merge_intake_staging", func() AdminActionResult {
		return h.executeMergeIntakeStagingAction(stagingPath)
	})
	h.setAdminResult(result)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"action_success": result.Success,
	})
}

func (h *Handlers) runAdminAction(action string, runner func() AdminActionResult) AdminActionResult {
	started := time.Now()
	if h.admin == nil {
		return newAdminResult(action, false, -1, started, "", []string{"admin is not configured"})
	}
	if !h.admin.actionMu.TryLock() {
		return newAdminResult(action, false, -1, started, "", []string{"another admin action is already running"})
	}
	defer h.admin.actionMu.Unlock()
	return runner()
}

func (h *Handlers) executeUpdateAction() AdminActionResult {
	started := time.Now()

	commands, err := h.buildUpdateCommands()
	if err != nil {
		return newAdminResult("update/build", false, -1, started, "", []string{err.Error()})
	}
	return h.runCommandSequence("update/build", commands)
}

func (h *Handlers) executeGenerateAction() AdminActionResult {
	generateBinary := filepath.Join(h.admin.cfg.ProjectRoot, "bin", "generate")
	commands := []adminCommand{
		{
			Path: generateBinary,
			Args: []string{
				"-data-dir", h.admin.cfg.DataDir,
				"-generated-dir", h.admin.cfg.GeneratedDir,
				"-informatics-creds-file", filepath.Join(h.admin.cfg.DataDir, "credentials", "informatics_credentials.json"),
				"-codeforces-creds-file", filepath.Join(h.admin.cfg.DataDir, "credentials", "codeforces_credentials.json"),
			},
		},
	}
	return h.runCommandSequence("generate", commands)
}

func (h *Handlers) executeCreateGroupAction(slug, name, formLink string) AdminActionResult {
	createGroupBinary := filepath.Join(h.admin.cfg.ProjectRoot, "bin", "create_group")
	commands := []adminCommand{
		{
			Path: createGroupBinary,
			Args: []string{
				"-data-dir", h.admin.cfg.DataDir,
				"-slug", slug,
				"-name", name,
				"-form-link", formLink,
			},
		},
	}
	return h.runCommandSequence("create_group", commands)
}

func (h *Handlers) executeMergeIntakeStagingAction(stagingPath string) AdminActionResult {
	mergeBinary := filepath.Join(h.admin.cfg.ProjectRoot, "bin", "merge_students")
	commands := []adminCommand{
		{
			Path: mergeBinary,
			Args: []string{
				"-data-dir", h.admin.cfg.DataDir,
				"-intake-file", stagingPath,
				"-write",
			},
		},
	}
	return h.runCommandSequence("merge_intake_staging", commands)
}

func (h *Handlers) runCommandSequence(action string, commands []adminCommand) AdminActionResult {
	started := time.Now()
	if len(commands) == 0 {
		return newAdminResult(action, false, -1, started, "", []string{"no commands configured"})
	}

	var output bytes.Buffer
	exitCode := 0
	errorsList := make([]string, 0)

	for idx, command := range commands {
		if idx > 0 {
			output.WriteString("\n")
		}
		output.WriteString("$ ")
		output.WriteString(renderCommand(command.Path, command.Args))
		output.WriteString("\n")

		cmd := exec.Command(command.Path, command.Args...)
		cmd.Dir = h.admin.cfg.ProjectRoot
		cmd.Stdout = &output
		cmd.Stderr = &output

		err := cmd.Run()
		if err != nil {
			exitCode = commandExitCode(err)
			errorsList = append(errorsList, fmt.Sprintf("command failed: %s (exit code %d)", renderCommand(command.Path, command.Args), exitCode))
			if exitCode < 0 {
				errorsList = append(errorsList, err.Error())
			}
			break
		}
	}

	success := len(errorsList) == 0
	return newAdminResult(action, success, exitCode, started, output.String(), errorsList)
}

func (h *Handlers) buildUpdateCommands() ([]adminCommand, error) {
	binDir := filepath.Join(h.admin.cfg.ProjectRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %q: %w", binDir, err)
	}

	commands := []adminCommand{{
		Path: "git",
		Args: []string{"pull"},
	}}

	targets := []string{"server", "generate", "create_group", "merge_students"}
	for _, target := range targets {
		cmdDir := filepath.Join(h.admin.cfg.ProjectRoot, "cmd", target)
		info, err := os.Stat(cmdDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat %q: %w", cmdDir, err)
		}
		if !info.IsDir() {
			continue
		}

		commands = append(commands, adminCommand{
			Path: "go",
			Args: []string{
				"build",
				"-o",
				"./bin/" + target,
				"./cmd/" + target,
			},
		})
	}

	commands = append(commands, adminCommand{
		Path: "sudo",
		Args: []string{"systemctl", "restart", "standings-edu"},
	})

	return commands, nil
}

func (h *Handlers) lastAdminResult() *AdminActionResult {
	if h.admin == nil {
		return nil
	}
	h.admin.resultMu.RLock()
	defer h.admin.resultMu.RUnlock()
	if h.admin.lastResult == nil {
		return nil
	}
	resultCopy := *h.admin.lastResult
	if len(resultCopy.Errors) > 0 {
		resultCopy.Errors = append([]string(nil), resultCopy.Errors...)
	}
	return &resultCopy
}

func (h *Handlers) setAdminResult(result AdminActionResult) {
	if h.admin == nil {
		return
	}
	h.admin.resultMu.Lock()
	defer h.admin.resultMu.Unlock()
	resultCopy := result
	if len(resultCopy.Errors) > 0 {
		resultCopy.Errors = append([]string(nil), resultCopy.Errors...)
	}
	h.admin.lastResult = &resultCopy
}

func (h *Handlers) listEditableFiles() ([]string, error) {
	if h.admin == nil {
		return nil, fmt.Errorf("admin is not configured")
	}

	files := []string{"data/students.json", "data/contests.json", adminIntakePath}
	groupsDir := filepath.Join(h.admin.cfg.DataDir, "groups")
	entries, err := os.ReadDir(groupsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return files, nil
		}
		return nil, fmt.Errorf("read groups dir: %w", err)
	}

	slugs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := strings.TrimSpace(entry.Name())
		if !domain.IsValidSlug(slug) {
			continue
		}
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		files = append(files,
			filepath.ToSlash(filepath.Join("data", "groups", slug, "group.json")),
			filepath.ToSlash(filepath.Join("data", "groups", slug, "contests.json")),
		)
	}

	return files, nil
}

func (h *Handlers) resolveEditablePath(path string) (string, string, error) {
	if h.admin == nil {
		return "", "", fmt.Errorf("admin is not configured")
	}

	path = strings.TrimSpace(path)
	path = strings.ReplaceAll(path, "\\", "/")

	switch path {
	case "data/students.json":
		return path, filepath.Join(h.admin.cfg.DataDir, "students.json"), nil
	case "data/contests.json":
		return path, filepath.Join(h.admin.cfg.DataDir, "contests.json"), nil
	case adminIntakePath:
		return path, filepath.Join(h.admin.cfg.DataDir, "student_intake.json"), nil
	case adminIntakeStagingPath:
		return path, filepath.Join(h.admin.cfg.DataDir, "student_intake_admin.json"), nil
	}

	const groupPrefix = "data/groups/"
	if !strings.HasPrefix(path, groupPrefix) {
		return "", "", fmt.Errorf("path %q is not allowed", path)
	}

	tail := strings.TrimPrefix(path, groupPrefix)
	parts := strings.Split(tail, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("path %q is not allowed", path)
	}

	slug := strings.TrimSpace(parts[0])
	fileName := strings.TrimSpace(parts[1])
	if !domain.IsValidSlug(slug) {
		return "", "", fmt.Errorf("path %q is not allowed", path)
	}
	if fileName != "group.json" && fileName != "contests.json" {
		return "", "", fmt.Errorf("path %q is not allowed", path)
	}

	normalized := filepath.ToSlash(filepath.Join("data", "groups", slug, fileName))
	absolute := filepath.Join(h.admin.cfg.DataDir, "groups", slug, fileName)
	return normalized, absolute, nil
}

func decodeAdminFileRequest(r *http.Request) (adminFileRequest, error) {
	var req adminFileRequest

	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAdminJSONBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return adminFileRequest{}, fmt.Errorf("invalid request body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return adminFileRequest{}, fmt.Errorf("request body must contain a single JSON object")
	}

	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		return adminFileRequest{}, fmt.Errorf("path is required")
	}

	return req, nil
}

func decodeAdminIntakeMergeRequest(r *http.Request) (adminIntakeMergeRequest, error) {
	var req adminIntakeMergeRequest

	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAdminJSONBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return adminIntakeMergeRequest{}, fmt.Errorf("invalid request body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return adminIntakeMergeRequest{}, fmt.Errorf("request body must contain a single JSON object")
	}

	return req, nil
}

func validateJSONSyntax(body string) error {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()

	var v any
	if err := decoder.Decode(&v); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid json: trailing data after root value")
	}

	return nil
}

func writeFileAtomically(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".admin-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmpFile.Write(body); err != nil {
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

func newAdminResult(action string, success bool, exitCode int, started time.Time, output string, errorsList []string) AdminActionResult {
	duration := time.Since(started).Round(time.Millisecond)
	if duration < 0 {
		duration = 0
	}
	if len(errorsList) == 0 {
		errorsList = nil
	}
	return AdminActionResult{
		Action:    action,
		Success:   success,
		ExitCode:  exitCode,
		StartedAt: started.Format("2006-01-02 15:04:05 MST"),
		Duration:  duration.String(),
		Output:    output,
		Errors:    errorsList,
	}
}

func commandExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func renderCommand(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteShellArg(path))
	for _, arg := range args {
		parts = append(parts, quoteShellArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteShellArg(value string) string {
	if value == "" {
		return "''"
	}
	if strings.ContainsAny(value, " \t\n\"'`$()[]{}|&;<>*?!") {
		return strconv.Quote(value)
	}
	return value
}
