package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
)

const MoodleGradesProviderID = "moodle_grades"

// moodleDefaultService — сервис для обмена логина/пароля на токен через
// /login/token.php (мобильный сервис включён почти на любом Moodle).
const moodleDefaultService = "moodle_mobile_app"

// MoodleCredentials — доступ к Moodle. Либо готовый token (создаётся в
// Администрирование → Сервер → Веб-сервисы → Управление ключами), либо
// username/password — тогда токен добывается автоматически через login/token.php.
type MoodleCredentials struct {
	BaseURL  string `json:"base_url"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Service  string `json:"service,omitempty"` // по умолчанию moodle_mobile_app
}

func LoadMoodleCredentials(path string) (MoodleCredentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return MoodleCredentials{}, fmt.Errorf("read moodle credentials: %w", err)
	}
	var creds MoodleCredentials
	if err := json.Unmarshal(b, &creds); err != nil {
		return MoodleCredentials{}, fmt.Errorf("decode moodle credentials: %w", err)
	}
	creds.BaseURL = strings.TrimRight(strings.TrimSpace(creds.BaseURL), "/")
	creds.Token = strings.TrimSpace(creds.Token)
	creds.Username = strings.TrimSpace(creds.Username)
	if creds.BaseURL == "" {
		return MoodleCredentials{}, fmt.Errorf("moodle credentials: base_url is required")
	}
	if creds.Token == "" && (creds.Username == "" || creds.Password == "") {
		return MoodleCredentials{}, fmt.Errorf("moodle credentials: either token or username+password is required")
	}
	if creds.Service == "" {
		creds.Service = moodleDefaultService
	}
	return creds, nil
}

// MoodleClient — минимальный клиент REST веб-сервисов Moodle
// (/webservice/rest/server.php, moodlewsrestformat=json).
type MoodleClient struct {
	httpClient *http.Client
	creds      MoodleCredentials

	mu    sync.Mutex
	token string
}

func NewMoodleClient(creds MoodleCredentials) *MoodleClient {
	return &MoodleClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		creds:      creds,
		token:      creds.Token,
	}
}

// BaseURL — базовый адрес Moodle (для валидации ссылок на активности).
func (c *MoodleClient) BaseURL() string { return c.creds.BaseURL }

type moodleWSError struct {
	Exception string `json:"exception"`
	ErrorCode string `json:"errorcode"`
	Message   string `json:"message"`
}

func (e moodleWSError) Error() string {
	return fmt.Sprintf("moodle ws error: %s (%s)", e.Message, e.ErrorCode)
}

// ensureToken возвращает токен: готовый из кредов или добытый через
// /login/token.php (username/password). force — перелогин при invalidtoken.
func (c *MoodleClient) ensureToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && !force {
		return c.token, nil
	}
	if c.creds.Username == "" || c.creds.Password == "" {
		if c.token != "" {
			// Токен задан статически: перелогиниться нечем.
			return c.token, nil
		}
		return "", fmt.Errorf("moodle: no token and no username/password to obtain one")
	}

	q := url.Values{}
	q.Set("username", c.creds.Username)
	q.Set("password", c.creds.Password)
	q.Set("service", c.creds.Service)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.creds.BaseURL+"/login/token.php?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	res, err := doHTTPWithRetry(c.httpClient, req, defaultHTTPRetryAttempts)
	if err != nil {
		return "", fmt.Errorf("moodle login: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("moodle login read: %w", err)
	}
	var parsed struct {
		Token     string `json:"token"`
		Error     string `json:"error"`
		ErrorCode string `json:"errorcode"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("moodle login decode: %w", err)
	}
	if parsed.Token == "" {
		return "", fmt.Errorf("moodle login failed: %s (%s)", parsed.Error, parsed.ErrorCode)
	}
	c.token = parsed.Token
	return c.token, nil
}

// call вызывает функцию веб-сервиса и декодирует ответ в out. Ответ-исключение
// Moodle превращается в ошибку; на invalidtoken при наличии логина/пароля токен
// добывается заново и вызов повторяется один раз.
func (c *MoodleClient) call(ctx context.Context, wsfunction string, params url.Values, out any) error {
	relogin := false
	for {
		token, err := c.ensureToken(ctx, relogin)
		if err != nil {
			return err
		}

		q := url.Values{}
		for k, vs := range params {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		q.Set("wstoken", token)
		q.Set("wsfunction", wsfunction)
		q.Set("moodlewsrestformat", "json")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.creds.BaseURL+"/webservice/rest/server.php?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		res, err := doHTTPWithRetry(c.httpClient, req, defaultHTTPRetryAttempts)
		if err != nil {
			return fmt.Errorf("moodle %s: %w", wsfunction, err)
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		res.Body.Close()
		if err != nil {
			return fmt.Errorf("moodle %s read: %w", wsfunction, err)
		}
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("moodle %s status=%d body=%q", wsfunction, res.StatusCode, truncateForError(body))
		}

		// Ответ-исключение — это JSON-объект с полем exception.
		var wsErr moodleWSError
		if json.Unmarshal(body, &wsErr) == nil && wsErr.Exception != "" {
			if wsErr.ErrorCode == "invalidtoken" && !relogin && c.creds.Username != "" {
				relogin = true
				continue
			}
			return fmt.Errorf("moodle %s: %w", wsfunction, wsErr)
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("moodle %s decode: %w body=%q", wsfunction, err, truncateForError(body))
		}
		return nil
	}
}

func truncateForError(body []byte) string {
	s := strings.TrimSpace(string(bytes.ToValidUTF8(body, nil)))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// --- Провайдер таблиц из журнала оценок ---

// MoodleGradesProvider строит таблицу контеста из журнала оценок Moodle по
// ссылке на активность (тест, задание — всё, что имеет оценку). Одна активность —
// одна колонка «задачи»: оценка ученика (по умолчанию в процентах от максимума),
// точная оценка «X из Y» — в колонке «Статус».
type MoodleGradesProvider struct {
	client *MoodleClient
}

func NewMoodleGradesProvider(client *MoodleClient) *MoodleGradesProvider {
	return &MoodleGradesProvider{client: client}
}

func (p *MoodleGradesProvider) ProviderID() string { return MoodleGradesProviderID }

// moodleGradesConfig — provider_config контеста moodle_grades.
type moodleGradesConfig struct {
	// ActivityURL — ссылка на активность: https://<moodle>/mod/<тип>/view.php?id=<cmid>.
	ActivityURL string `json:"activity_url"`
	// MoodleGroup — группа Moodle (название или числовой id); пусто — весь курс.
	MoodleGroup string `json:"moodle_group,omitempty"`
	// ShowAll — показывать всех пользователей курса/группы Moodle: сопоставленные
	// по ФИО привязываются к ученикам, остальные добавляются отдельными строками.
	// false — только ученики группы standings (вариант «автопоиск по ФИО»).
	ShowAll bool `json:"show_all,omitempty"`
	// Display — как показывать оценку в ячейке: "percent" (по умолчанию,
	// 0..100 от максимума — работает цветовая заливка) или "raw" (как в журнале).
	Display string `json:"display,omitempty"`
}

func parseMoodleGradesConfig(raw json.RawMessage) (moodleGradesConfig, error) {
	var cfg moodleGradesConfig
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return cfg, fmt.Errorf("provider_config is required for provider=%q", MoodleGradesProviderID)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode provider_config: %w", err)
	}
	cfg.ActivityURL = strings.TrimSpace(cfg.ActivityURL)
	if cfg.ActivityURL == "" {
		return cfg, fmt.Errorf("provider_config.activity_url is required")
	}
	cfg.Display = strings.ToLower(strings.TrimSpace(cfg.Display))
	switch cfg.Display {
	case "", "percent":
		cfg.Display = "percent"
	case "raw":
	default:
		return cfg, fmt.Errorf("provider_config.display must be \"percent\" or \"raw\", got %q", cfg.Display)
	}
	return cfg, nil
}

// parseMoodleActivityURL достаёт cmid из ссылки на активность
// (…/mod/<тип>/view.php?id=<cmid>).
func parseMoodleActivityURL(rawURL string) (int, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0, fmt.Errorf("activity_url: %w", err)
	}
	path := strings.ToLower(strings.TrimSpace(u.Path))
	if !strings.HasPrefix(path, "/mod/") || !strings.HasSuffix(path, "/view.php") {
		return 0, fmt.Errorf("activity_url must look like https://<moodle>/mod/<type>/view.php?id=<cmid>, got path %q", u.Path)
	}
	cmid, err := strconv.Atoi(strings.TrimSpace(u.Query().Get("id")))
	if err != nil || cmid <= 0 {
		return 0, fmt.Errorf("activity_url must contain numeric id= (course module id)")
	}
	return cmid, nil
}

type moodleCourseModule struct {
	CM struct {
		ID       int    `json:"id"`
		Course   int    `json:"course"`
		Name     string `json:"name"`
		ModName  string `json:"modname"`
		Instance int    `json:"instance"`
	} `json:"cm"`
}

type moodleGradeReport struct {
	UserGrades []struct {
		UserID       int    `json:"userid"`
		UserFullName string `json:"userfullname"`
		GradeItems   []struct {
			CmID     *int     `json:"cmid"`
			ItemType string   `json:"itemtype"`
			GradeRaw *float64 `json:"graderaw"`
			GradeMax float64  `json:"grademax"`
		} `json:"gradeitems"`
	} `json:"usergrades"`
}

type moodleCourseGroup struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// moodleUserGrade — оценка одного пользователя Moodle по выбранной активности.
type moodleUserGrade struct {
	FullName string
	Graded   bool
	Raw      float64
	Max      float64
}

func (p *MoodleGradesProvider) BuildStandings(ctx context.Context, input ContestProviderInput) (domain.GeneratedContestStandings, error) {
	if p == nil || p.client == nil {
		return domain.GeneratedContestStandings{}, fmt.Errorf("moodle provider is not configured (no credentials)")
	}
	cfg, err := parseMoodleGradesConfig(input.Contest.ProviderConfig)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}
	cmid, err := parseMoodleActivityURL(cfg.ActivityURL)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	// Модуль курса: курс и человекочитаемое название активности.
	var cm moodleCourseModule
	if err := p.client.call(ctx, "core_course_get_course_module", url.Values{"cmid": {strconv.Itoa(cmid)}}, &cm); err != nil {
		return domain.GeneratedContestStandings{}, err
	}
	if cm.CM.Course <= 0 {
		return domain.GeneratedContestStandings{}, fmt.Errorf("moodle cmid=%d: course not resolved", cmid)
	}

	groupID, err := p.resolveMoodleGroup(ctx, cm.CM.Course, cfg.MoodleGroup)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	// Журнал оценок курса (при groupID>0 — только группа Moodle).
	params := url.Values{"courseid": {strconv.Itoa(cm.CM.Course)}}
	if groupID > 0 {
		params.Set("groupid", strconv.Itoa(groupID))
	}
	var report moodleGradeReport
	if err := p.client.call(ctx, "gradereport_user_get_grade_items", params, &report); err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	grades, gradeMax := extractMoodleGrades(report, cmid)
	if len(grades) == 0 {
		return domain.GeneratedContestStandings{}, fmt.Errorf("moodle cmid=%d course=%d: no users in grade report (check rights of the token user)", cmid, cm.CM.Course)
	}

	return buildMoodleStandings(input.Contest, cfg, cm.CM.Name, gradeMax, grades, input.Students), nil
}

// resolveMoodleGroup превращает название/id группы Moodle в groupid.
// Пустое значение — без фильтра (0).
func (p *MoodleGradesProvider) resolveMoodleGroup(ctx context.Context, courseID int, group string) (int, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return 0, nil
	}
	if id, err := strconv.Atoi(group); err == nil && id > 0 {
		return id, nil
	}
	var groups []moodleCourseGroup
	if err := p.client.call(ctx, "core_group_get_course_groups", url.Values{"courseid": {strconv.Itoa(courseID)}}, &groups); err != nil {
		return 0, fmt.Errorf("resolve moodle_group %q: %w", group, err)
	}
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		if strings.EqualFold(strings.TrimSpace(g.Name), group) {
			return g.ID, nil
		}
		names = append(names, g.Name)
	}
	return 0, fmt.Errorf("moodle_group %q not found in course %d (groups: %s)", group, courseID, strings.Join(names, ", "))
}

// extractMoodleGrades выбирает из журнала оценку каждого пользователя по
// активности cmid. Возвращает пользователей (включая без оценки) и максимум.
func extractMoodleGrades(report moodleGradeReport, cmid int) ([]moodleUserGrade, float64) {
	out := make([]moodleUserGrade, 0, len(report.UserGrades))
	gradeMax := 0.0
	for _, ug := range report.UserGrades {
		name := strings.TrimSpace(ug.UserFullName)
		if name == "" {
			continue
		}
		entry := moodleUserGrade{FullName: name}
		for _, gi := range ug.GradeItems {
			if gi.ItemType == "course" || gi.CmID == nil || *gi.CmID != cmid {
				continue
			}
			entry.Max = gi.GradeMax
			if gi.GradeMax > gradeMax {
				gradeMax = gi.GradeMax
			}
			if gi.GradeRaw != nil {
				entry.Graded = true
				entry.Raw = *gi.GradeRaw
			}
			break
		}
		out = append(out, entry)
	}
	return out, gradeMax
}

// matchMoodleUsers сопоставляет пользователей Moodle ученикам по ФИО. Moodle
// отдаёт «Имя Фамилия», в standings хранится «Фамилия Имя Отчество», поэтому
// сравнение пословное, без учёта порядка: каждый токен имени Moodle должен
// найтись среди токенов ФИО ученика (инициал «я.» совпадает с «яна»). При
// равном качестве совпадения у двух учеников пользователь остаётся
// несопоставленным (тёзки — безопаснее показать отдельной строкой).
func matchMoodleUsers(grades []moodleUserGrade, students []domain.Student) map[int]string {
	type candidate struct {
		id     string
		tokens []string
	}
	candidates := make([]candidate, 0, len(students))
	for _, s := range students {
		tokens := uniqueStrings(append(
			strings.Fields(normalizeForMatch(s.FullName)),
			strings.Fields(normalizeForMatch(s.PublicName))...,
		))
		if len(tokens) == 0 {
			continue
		}
		candidates = append(candidates, candidate{id: s.ID, tokens: tokens})
	}

	type claim struct {
		gi    int
		score int
	}
	out := make(map[int]string)
	taken := make(map[string]claim) // studentID -> лучшая строка Moodle
	for gi, g := range grades {
		nameTokens := strings.Fields(normalizeForMatch(g.FullName))
		if len(nameTokens) < 2 {
			continue
		}
		bestID := ""
		bestScore := 0
		tie := false
		for _, c := range candidates {
			score, ok := moodleNameMatchScore(nameTokens, c.tokens)
			if !ok {
				continue
			}
			switch {
			case score > bestScore:
				bestScore, bestID, tie = score, c.id, false
			case score == bestScore && bestScore > 0 && c.id != bestID:
				tie = true
			}
		}
		if bestID == "" || tie {
			continue
		}
		// Один ученик — одна строка Moodle: при повторном совпадении побеждает
		// более качественное (полное) совпадение.
		if prev, ok := taken[bestID]; ok {
			if bestScore <= prev.score {
				continue
			}
			delete(out, prev.gi)
		}
		taken[bestID] = claim{gi: gi, score: bestScore}
		out[gi] = bestID
	}
	return out
}

// moodleNameMatchScore — качество совпадения имени Moodle с токенами ФИО
// ученика: каждый токен имени должен совпасть со своим токеном ФИО, каждый
// токен ФИО используется один раз. Точное совпадение слова даёт его длину в
// очках, совпадение по инициалу («я.» ↔ «яна») — только 1 очко: инициал —
// слабое свидетельство и не должен перебивать настоящую фамилию другого
// ученика. Требуется минимум два точных совпадения слов; если полных слов
// (не инициалов) у ученика или в имени Moodle меньше — по их числу, но
// хотя бы одно (случай ученика только с public name «Петров В.»).
func moodleNameMatchScore(nameTokens, studentTokens []string) (int, bool) {
	isInitial := func(s string) bool {
		r := []rune(s)
		return len(r) == 2 && r[1] == '.'
	}

	used := make([]bool, len(studentTokens))
	score := 0
	fullMatches := 0
	for _, nt := range nameTokens {
		matched := false
		// Сначала точное слово, потом инициал — чтобы инициал не «съел» токен,
		// который мог совпасть точно.
		if !isInitial(nt) {
			for i, st := range studentTokens {
				if !used[i] && nt == st {
					used[i] = true
					matched = true
					score += len([]rune(nt))
					fullMatches++
					break
				}
			}
		}
		if !matched {
			for i, st := range studentTokens {
				if !used[i] && moodleInitialMatch(nt, st) {
					used[i] = true
					matched = true
					score++
					break
				}
			}
		}
		if !matched {
			return 0, false
		}
	}

	studentFull := 0
	for _, st := range studentTokens {
		if !isInitial(st) {
			studentFull++
		}
	}
	nameFull := 0
	for _, nt := range nameTokens {
		if !isInitial(nt) {
			nameFull++
		}
	}
	required := 2
	if nameFull < required {
		required = nameFull
	}
	if studentFull < required {
		required = studentFull
	}
	if required < 1 {
		required = 1
	}
	if len(nameTokens) < 2 || fullMatches < required {
		return 0, false
	}
	return score, true
}

// moodleInitialMatch — совпадение инициала с полным словом в любую сторону
// («я.» ↔ «яна»); пара одинаковых инициалов тоже считается (слабым) совпадением.
func moodleInitialMatch(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	isInitial := func(r []rune) bool { return len(r) == 2 && r[1] == '.' }
	switch {
	case isInitial(ra) && isInitial(rb):
		return ra[0] == rb[0]
	case isInitial(ra) && len(rb) > 1:
		return ra[0] == rb[0]
	case isInitial(rb) && len(ra) > 1:
		return rb[0] == ra[0]
	}
	return false
}

// formatMoodleGrade — «человеческий» вид оценки: без хвостовых нулей,
// с запятой как в журнале («7,5», «10»).
func formatMoodleGrade(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return strings.ReplaceAll(s, ".", ",")
}

// buildMoodleStandings собирает таблицу: одна колонка-«задача» (сама
// активность), строки — ученики группы (+ при show_all все остальные
// пользователи Moodle отдельными строками).
func buildMoodleStandings(
	contest domain.Contest,
	cfg moodleGradesConfig,
	activityName string,
	gradeMax float64,
	grades []moodleUserGrade,
	students []domain.Student,
) domain.GeneratedContestStandings {
	isIOI := contest.ScoreSystem.IsIOI()

	subTitle := strings.TrimSpace(activityName)
	if subTitle == "" {
		subTitle = "Оценка"
	}
	if gradeMax > 0 {
		subTitle += " (из " + formatMoodleGrade(gradeMax) + ")"
	}
	task := domain.GeneratedTask{
		Label:         "A",
		URL:           cfg.ActivityURL,
		NormalizedURL: domain.NormalizeTaskURL(cfg.ActivityURL),
	}
	out := domain.GeneratedContestStandings{
		ID:          contest.ID,
		Title:       contest.Title,
		ScoreSystem: contest.ScoreSystem.Normalized(),
		Subcontests: []domain.GeneratedSubcontest{{
			Title:     subTitle,
			TaskCount: 1,
			Tasks:     []domain.GeneratedTask{task},
		}},
		Tasks: []domain.GeneratedTask{task},
		Rows:  make([]domain.GeneratedRow, 0, len(students)),
	}

	matched := matchMoodleUsers(grades, students)
	gradeByStudent := make(map[string]moodleUserGrade, len(matched))
	for gi, sid := range matched {
		gradeByStudent[sid] = grades[gi]
	}

	appendRow := func(studentID, publicName string, g *moodleUserGrade) {
		row := domain.GeneratedRow{
			StudentID:  studentID,
			PublicName: publicName,
			Statuses:   []string{domain.TaskStatusNone},
		}
		if isIOI {
			row.Scores = []*int{nil}
		}
		if g != nil && g.Graded {
			score := int(math.Round(g.Raw))
			if cfg.Display == "percent" && g.Max > 0 {
				score = int(math.Round(g.Raw / g.Max * 100))
			}
			solved := g.Max > 0 && g.Raw >= g.Max
			if solved {
				row.Statuses[0] = domain.TaskStatusSolved
				row.SolvedCount = 1
			} else {
				row.Statuses[0] = domain.TaskStatusAttempted
			}
			if isIOI {
				row.Scores[0] = &score
				row.TotalScore = score
			}
			row.ProviderStatus = formatMoodleGrade(g.Raw) + " из " + formatMoodleGrade(g.Max)
		}
		out.Rows = append(out.Rows, row)
	}

	for _, student := range students {
		if g, ok := gradeByStudent[student.ID]; ok {
			appendRow(student.ID, student.PublicName, &g)
		} else {
			appendRow(student.ID, student.PublicName, nil)
		}
	}

	if cfg.ShowAll {
		matchedRows := make(map[int]struct{}, len(matched))
		for gi := range matched {
			matchedRows[gi] = struct{}{}
		}
		extra := 0
		for gi := range grades {
			if _, ok := matchedRows[gi]; ok {
				continue
			}
			extra++
			g := grades[gi]
			appendRow(fmt.Sprintf("moodle_extra_row_%d", extra), g.FullName, &g)
		}
	}

	sortMoodleRows(out.Rows, isIOI)
	return out
}

// sortMoodleRows сортирует строки по оценке по убыванию (без оценки — вниз),
// при равенстве — по имени.
func sortMoodleRows(rows []domain.GeneratedRow, isIOI bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		gi, gj := rows[i].ProviderStatus != "", rows[j].ProviderStatus != ""
		if gi != gj {
			return gi
		}
		if isIOI && rows[i].TotalScore != rows[j].TotalScore {
			return rows[i].TotalScore > rows[j].TotalScore
		}
		if rows[i].SolvedCount != rows[j].SolvedCount {
			return rows[i].SolvedCount > rows[j].SolvedCount
		}
		return strings.ToLower(rows[i].PublicName) < strings.ToLower(rows[j].PublicName)
	})
}
