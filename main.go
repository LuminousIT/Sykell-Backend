package main

// go get github.com/gin-gonic/gin
// go get github.com/PuerkitoBio/goquery
// go get github.com/golang-jwt/jwt/v5
// go get github.com/gorilla/websocket
// go get github.com/lib/pq
// go get golang.org/x/crypto/bcrypt
import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	_ "github.com/lib/pq" // PostgreSQL driver
	"golang.org/x/crypto/bcrypt"
)

// Configuration
const (
	JWTSecret      = "your-secret-key-change-this-in-production"
	RequestTimeout = 30 * time.Second
	MaxWorkers     = 5
	CacheExpiry    = 1 * time.Hour // Cache crawl results for 1 hour
)

// Job Status Constants
const (
	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusCompleted = "completed"
	JobStatusFailed    = "failed"
	JobStatusCancelled = "cancelled"
	JobStatusStopped   = "stopped"
)

// Database Models
type User struct {
	ID        int       `json:"id" db:"id"`
	Username  string    `json:"username" db:"username"`
	Email     string    `json:"email" db:"email"`
	Password  string    `json:"-" db:"password_hash"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

type CrawlJob struct {
	ID          string        `json:"id" db:"id"`
	UserID      int           `json:"user_id" db:"user_id"`
	URLs        []string      `json:"urls" db:"urls"`
	Status      string        `json:"status" db:"status"`
	Progress    int           `json:"progress" db:"progress"`
	TotalURLs   int           `json:"total_urls" db:"total_urls"`
	CreatedAt   time.Time     `json:"created_at" db:"created_at"`
	StartedAt   *time.Time    `json:"started_at,omitempty" db:"started_at"`
	CompletedAt *time.Time    `json:"completed_at,omitempty" db:"completed_at"`
	Error       string        `json:"error,omitempty" db:"error"`
	Results     []CrawlResult `json:"results,omitempty"`
}

type CrawlResult struct {
	ID            int            `json:"id" db:"id"`
	JobID         string         `json:"job_id" db:"job_id"`
	UserID        int            `json:"user_id" db:"user_id"`
	URL           string         `json:"url" db:"url"`
	HTMLVersion   string         `json:"html_version" db:"html_version"`
	Title         string         `json:"title" db:"title"`
	HeadingCounts map[string]int `json:"heading_counts" db:"heading_counts"`
	LinkAnalysis  LinkAnalysis   `json:"link_analysis" db:"link_analysis"`
	HasLoginForm  bool           `json:"has_login_form" db:"has_login_form"`
	Error         string         `json:"error,omitempty" db:"error"`
	CrawledAt     time.Time      `json:"crawled_at" db:"crawled_at"`
}

// Request/Response Models
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=50"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type CrawlRequest struct {
	URLs []string `json:"urls" binding:"required,min=1,max=50"`
}

type JobControlRequest struct {
	JobID  string `json:"job_id,omitempty"`
	Action string `json:"action" binding:"required"` // "stop", "start", "cancel"
}

type RerunRequest struct {
	URLs []string `json:"urls" binding:"required,min=1,max=50"`
}

type DeleteRequest struct {
	URLs    []string `json:"urls,omitempty"`
	JobIDs  []string `json:"job_ids,omitempty"`
	Results []int    `json:"result_ids,omitempty"` // Individual result IDs
}

type HeadingCount struct {
	H1 int `json:"h1"`
	H2 int `json:"h2"`
	H3 int `json:"h3"`
	H4 int `json:"h4"`
	H5 int `json:"h5"`
	H6 int `json:"h6"`
}

type InaccessibleLink struct {
	LinkURL   string `json:"link_url"`
	ErrorCode int    `json:"error_code"`
}

type LinkAnalysis struct {
	InternalLinks     int                `json:"internal_links"`
	ExternalLinks     int                `json:"external_links"`
	InaccessibleLinks []InaccessibleLink `json:"inaccessible_links"`
}

type PageInfo struct {
	URL           string       `json:"url"`
	HTMLVersion   string       `json:"html_version"`
	Title         string       `json:"title"`
	HeadingCounts HeadingCount `json:"heading_counts"`
	LinkAnalysis  LinkAnalysis `json:"link_analysis"`
	HasLoginForm  bool         `json:"has_login_form"`
	Error         string       `json:"error,omitempty"`
	CrawledAt     time.Time    `json:"crawled_at"`
}

// WebSocket Message Types
type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type JobUpdate struct {
	JobID       string     `json:"job_id"`
	Status      string     `json:"status"`
	Progress    int        `json:"progress"`
	TotalURLs   int        `json:"total_urls"`
	CurrentURL  string     `json:"current_url,omitempty"`
	Result      *PageInfo  `json:"result,omitempty"`
	Error       string     `json:"error,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// JWT Claims
type Claims struct {
	UserID   int    `json:"user_id"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// WebSocket Connection Manager
type ConnectionManager struct {
	connections map[int]*websocket.Conn // userID -> connection
	mu          sync.RWMutex
	upgrader    websocket.Upgrader
}

func NewConnectionManager() *ConnectionManager {
	return &ConnectionManager{
		connections: make(map[int]*websocket.Conn),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins in development
			},
		},
	}
}

func (cm *ConnectionManager) AddConnection(userID int, conn *websocket.Conn) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Close existing connection if any
	if existingConn, exists := cm.connections[userID]; exists {
		existingConn.Close()
	}

	cm.connections[userID] = conn
}

func (cm *ConnectionManager) RemoveConnection(userID int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if conn, exists := cm.connections[userID]; exists {
		conn.Close()
		delete(cm.connections, userID)
	}
}

func (cm *ConnectionManager) SendToUser(userID int, message WSMessage) {
	cm.mu.RLock()
	conn, exists := cm.connections[userID]
	cm.mu.RUnlock()

	if !exists {
		return
	}

	if err := conn.WriteJSON(message); err != nil {
		log.Printf("Error sending message to user %d: %v", userID, err)
		cm.RemoveConnection(userID)
	}
}

func (cm *ConnectionManager) BroadcastToUser(userID int, msgType string, data interface{}) {
	cm.SendToUser(userID, WSMessage{
		Type: msgType,
		Data: data,
	})
}

// Job Manager
type JobManager struct {
	jobs        map[string]*CrawlJob
	jobContexts map[string]context.CancelFunc
	mu          sync.RWMutex
	db          *DatabaseService
	crawler     *WebCrawler
	connMgr     *ConnectionManager
}

func NewJobManager(db *DatabaseService, crawler *WebCrawler, connMgr *ConnectionManager) *JobManager {
	return &JobManager{
		jobs:        make(map[string]*CrawlJob),
		jobContexts: make(map[string]context.CancelFunc),
		db:          db,
		crawler:     crawler,
		connMgr:     connMgr,
	}
}

func (jm *JobManager) CreateJob(userID int, urls []string) (*CrawlJob, error) {
	job := &CrawlJob{
		ID:        fmt.Sprintf("job_%d_%d", userID, time.Now().Unix()),
		UserID:    userID,
		URLs:      urls,
		Status:    JobStatusPending,
		Progress:  0,
		TotalURLs: len(urls),
		CreatedAt: time.Now(),
	}

	// Save to database
	if err := jm.db.CreateCrawlJob(job); err != nil {
		return nil, err
	}

	jm.mu.Lock()
	jm.jobs[job.ID] = job
	jm.mu.Unlock()

	return job, nil
}

func (jm *JobManager) StartJob(jobID string) error {
	jm.mu.RLock()
	job, exists := jm.jobs[jobID]
	jm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job not found")
	}

	if job.Status != JobStatusPending {
		return fmt.Errorf("job is not in pending status")
	}

	ctx, cancel := context.WithCancel(context.Background())
	jm.mu.Lock()
	jm.jobContexts[jobID] = cancel
	jm.mu.Unlock()

	// Start job in goroutine
	go jm.runJob(ctx, job)

	return nil
}

func (jm *JobManager) StopJob(jobID string) error {
	jm.mu.RLock()
	job, exists := jm.jobs[jobID]
	cancel, hasContext := jm.jobContexts[jobID]
	jm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job not found")
	}

	if job.Status != JobStatusRunning {
		return fmt.Errorf("job is not running")
	}

	if hasContext {
		cancel()
	}

	job.Status = JobStatusStopped
	jm.db.UpdateCrawlJob(job)

	// Notify user
	jm.connMgr.BroadcastToUser(job.UserID, "job_update", JobUpdate{
		JobID:     job.ID,
		Status:    job.Status,
		Progress:  job.Progress,
		TotalURLs: job.TotalURLs,
	})

	return nil
}

func (jm *JobManager) StopAllJobs(userID int) error {
	jm.mu.RLock()
	var userJobs []*CrawlJob
	for _, job := range jm.jobs {
		if job.UserID == userID && job.Status == JobStatusRunning {
			userJobs = append(userJobs, job)
		}
	}
	jm.mu.RUnlock()

	for _, job := range userJobs {
		jm.StopJob(job.ID)
	}

	return nil
}

func (jm *JobManager) GetUserJobs(userID int) []*CrawlJob {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	var userJobs []*CrawlJob
	for _, job := range jm.jobs {
		if job.UserID == userID {
			userJobs = append(userJobs, job)
		}
	}

	return userJobs
}

func (jm *JobManager) runJob(ctx context.Context, job *CrawlJob) {
	// Update job status
	now := time.Now()
	job.Status = JobStatusRunning
	job.StartedAt = &now
	jm.db.UpdateCrawlJob(job)

	// Notify user
	jm.connMgr.BroadcastToUser(job.UserID, "job_update", JobUpdate{
		JobID:     job.ID,
		Status:    job.Status,
		Progress:  job.Progress,
		TotalURLs: job.TotalURLs,
	})

	// Process URLs
	for i, url := range job.URLs {
		select {
		case <-ctx.Done():
			// Job was cancelled
			return
		default:
			// Notify current URL being processed
			jm.connMgr.BroadcastToUser(job.UserID, "job_update", JobUpdate{
				JobID:      job.ID,
				Status:     job.Status,
				Progress:   i,
				TotalURLs:  job.TotalURLs,
				CurrentURL: url,
			})

			// Crawl the page
			result := jm.crawler.CrawlPage(job.UserID, url)

			// Save result to database
			jm.db.SaveCrawlResultWithJob(job.ID, job.UserID, result)

			// Update progress
			job.Progress = i + 1
			jm.db.UpdateCrawlJob(job)

			// Notify user with result
			jm.connMgr.BroadcastToUser(job.UserID, "job_update", JobUpdate{
				JobID:     job.ID,
				Status:    job.Status,
				Progress:  job.Progress,
				TotalURLs: job.TotalURLs,
				Result:    &result,
			})
		}
	}

	// Job completed
	now = time.Now()
	job.Status = JobStatusCompleted
	job.CompletedAt = &now
	jm.db.UpdateCrawlJob(job)

	// Final notification
	jm.connMgr.BroadcastToUser(job.UserID, "job_update", JobUpdate{
		JobID:       job.ID,
		Status:      job.Status,
		Progress:    job.Progress,
		TotalURLs:   job.TotalURLs,
		CompletedAt: &now,
	})

	// Clean up context
	jm.mu.Lock()
	delete(jm.jobContexts, job.ID)
	jm.mu.Unlock()
}

// Database Service (Enhanced)
type DatabaseService struct {
	db *sql.DB
}

func NewDatabaseService(databaseURL string) (*DatabaseService, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	service := &DatabaseService{db: db}
	if err := service.createTables(); err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	return service, nil
}

func (ds *DatabaseService) createTables() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id SERIAL PRIMARY KEY,
			username VARCHAR(50) UNIQUE NOT NULL,
			email VARCHAR(100) UNIQUE NOT NULL,
			password_hash VARCHAR(255) NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS crawl_jobs (
			id VARCHAR(100) PRIMARY KEY,
			user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
			urls JSONB NOT NULL,
			status VARCHAR(20) DEFAULT 'pending',
			progress INTEGER DEFAULT 0,
			total_urls INTEGER NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			started_at TIMESTAMP,
			completed_at TIMESTAMP,
			error TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS crawl_results (
			id SERIAL PRIMARY KEY,
			job_id VARCHAR(100) REFERENCES crawl_jobs(id) ON DELETE CASCADE,
			user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
			url TEXT NOT NULL,
			html_version VARCHAR(20),
			title TEXT,
			heading_counts JSONB,
			link_analysis JSONB,
			has_login_form BOOLEAN DEFAULT FALSE,
			error TEXT,
			crawled_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_crawl_jobs_user_id ON crawl_jobs(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_crawl_jobs_status ON crawl_jobs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_crawl_results_job_id ON crawl_results(job_id)`,
		`CREATE INDEX IF NOT EXISTS idx_crawl_results_user_id ON crawl_results(user_id)`,
	}

	for _, query := range queries {
		if _, err := ds.db.Exec(query); err != nil {
			return fmt.Errorf("failed to execute query: %s, error: %w", query, err)
		}
	}

	return nil
}

func (ds *DatabaseService) CreateCrawlJob(job *CrawlJob) error {
	urlsJSON, _ := json.Marshal(job.URLs)

	query := `INSERT INTO crawl_jobs (id, user_id, urls, status, progress, total_urls, created_at) 
			  VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := ds.db.Exec(query, job.ID, job.UserID, urlsJSON, job.Status, job.Progress, job.TotalURLs, job.CreatedAt)
	return err
}

func (ds *DatabaseService) UpdateCrawlJob(job *CrawlJob) error {
	query := `UPDATE crawl_jobs 
			  SET status = $1, progress = $2, started_at = $3, completed_at = $4, error = $5 
			  WHERE id = $6`

	_, err := ds.db.Exec(query, job.Status, job.Progress, job.StartedAt, job.CompletedAt, job.Error, job.ID)
	return err
}

func (ds *DatabaseService) GetCrawlJobsByUser(userID int) ([]CrawlJob, error) {
	query := `SELECT id, user_id, urls, status, progress, total_urls, created_at, started_at, completed_at, error 
			  FROM crawl_jobs 
			  WHERE user_id = $1 
			  ORDER BY created_at DESC`

	rows, err := ds.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []CrawlJob
	jobIDs := []string{}

	for rows.Next() {
		var job CrawlJob
		var urlsJSON []byte
		var startedAt, completedAt sql.NullTime
		var errorStr sql.NullString

		err := rows.Scan(&job.ID, &job.UserID, &urlsJSON, &job.Status, &job.Progress,
			&job.TotalURLs, &job.CreatedAt, &startedAt, &completedAt, &errorStr)
		if err != nil {
			return nil, err
		}

		json.Unmarshal(urlsJSON, &job.URLs)
		if startedAt.Valid {
			job.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
		}
		if errorStr.Valid {
			job.Error = errorStr.String
		}

		jobs = append(jobs, job)
		jobIDs = append(jobIDs, job.ID)
	}

	// Get results for all jobs
	if len(jobIDs) > 0 {
		resultsMap, err := ds.getCrawlResultsByJobIDs(jobIDs)
		if err != nil {
			return nil, err
		}

		// Attach results to jobs
		for i := range jobs {
			if results, exists := resultsMap[jobs[i].ID]; exists {
				jobs[i].Results = results
			}
		}
	}

	return jobs, nil
}

// Enhanced method to get current jobs with their results for JobManager
func (ds *DatabaseService) GetCurrentJobsWithResults(userID int) ([]CrawlJob, error) {
	// Get jobs that are currently active (pending, running)
	jobQuery := `SELECT id, user_id, urls, status, progress, total_urls, created_at, started_at, completed_at, error 
				 FROM crawl_jobs 
				 WHERE user_id = $1 AND status IN ('pending', 'running')
				 ORDER BY created_at DESC`

	rows, err := ds.db.Query(jobQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []CrawlJob
	jobIDs := []string{}

	for rows.Next() {
		var job CrawlJob
		var urlsJSON []byte
		var startedAt, completedAt sql.NullTime
		var errorStr sql.NullString

		err := rows.Scan(&job.ID, &job.UserID, &urlsJSON, &job.Status, &job.Progress,
			&job.TotalURLs, &job.CreatedAt, &startedAt, &completedAt, &errorStr)
		if err != nil {
			return nil, err
		}

		json.Unmarshal(urlsJSON, &job.URLs)
		if startedAt.Valid {
			job.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
		}
		if errorStr.Valid {
			job.Error = errorStr.String
		}

		jobs = append(jobs, job)
		jobIDs = append(jobIDs, job.ID)
	}

	// Get results for all jobs
	if len(jobIDs) > 0 {
		resultsMap, err := ds.getCrawlResultsByJobIDs(jobIDs)
		if err != nil {
			return nil, err
		}

		// Attach results to jobs
		for i := range jobs {
			if results, exists := resultsMap[jobs[i].ID]; exists {
				jobs[i].Results = results
			}
		}
	}

	return jobs, nil
}

// Helper method to get crawl results by job IDs
func (ds *DatabaseService) getCrawlResultsByJobIDs(jobIDs []string) (map[string][]CrawlResult, error) {
	if len(jobIDs) == 0 {
		return make(map[string][]CrawlResult), nil
	}

	// Create placeholder string for IN clause
	placeholders := make([]string, len(jobIDs))
	args := make([]interface{}, len(jobIDs))
	for i, jobID := range jobIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = jobID
	}

	query := fmt.Sprintf(`SELECT id, job_id, user_id, url, html_version, title, heading_counts, link_analysis, has_login_form, error, crawled_at
						  FROM crawl_results 
						  WHERE job_id IN (%s)
						  ORDER BY crawled_at ASC`, strings.Join(placeholders, ","))

	rows, err := ds.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resultsMap := make(map[string][]CrawlResult)

	for rows.Next() {
		var result CrawlResult
		var headingCountsJSON, linkAnalysisJSON []byte
		var errorStr sql.NullString

		err := rows.Scan(
			&result.ID, &result.JobID, &result.UserID, &result.URL, &result.HTMLVersion, &result.Title,
			&headingCountsJSON, &linkAnalysisJSON, &result.HasLoginForm,
			&errorStr, &result.CrawledAt,
		)
		if err != nil {
			return nil, err
		}

		if errorStr.Valid {
			result.Error = errorStr.String
		}

		json.Unmarshal(headingCountsJSON, &result.HeadingCounts)
		json.Unmarshal(linkAnalysisJSON, &result.LinkAnalysis)

		resultsMap[result.JobID] = append(resultsMap[result.JobID], result)
	}

	return resultsMap, nil
}

func (ds *DatabaseService) SaveCrawlResultWithJob(jobID string, userID int, pageInfo PageInfo) error {
	headingCountsJSON, _ := json.Marshal(pageInfo.HeadingCounts)
	linkAnalysisJSON, _ := json.Marshal(pageInfo.LinkAnalysis)

	query := `INSERT INTO crawl_results 
			  (job_id, user_id, url, html_version, title, heading_counts, link_analysis, has_login_form, error, crawled_at) 
			  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	errorStr := ""
	if pageInfo.Error != "" {
		errorStr = pageInfo.Error
	}

	_, err := ds.db.Exec(query, jobID, userID, pageInfo.URL, pageInfo.HTMLVersion, pageInfo.Title,
		headingCountsJSON, linkAnalysisJSON, pageInfo.HasLoginForm, errorStr, pageInfo.CrawledAt)
	return err
}

// Include all the existing database methods (CreateUser, GetUserByUsername, etc.)
func (ds *DatabaseService) CreateUser(username, email, password string) (*User, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	query := `INSERT INTO users (username, email, password_hash) 
			  VALUES ($1, $2, $3) 
			  RETURNING id, username, email, created_at, updated_at`

	user := &User{}
	err = ds.db.QueryRow(query, username, email, string(hashedPassword)).Scan(
		&user.ID, &user.Username, &user.Email, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	return user, nil
}

func (ds *DatabaseService) GetUserByUsername(username string) (*User, error) {
	query := `SELECT id, username, email, password_hash, created_at, updated_at 
			  FROM users WHERE username = $1`

	user := &User{}
	err := ds.db.QueryRow(query, username).Scan(
		&user.ID, &user.Username, &user.Email, &user.Password, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	return user, nil
}

func (ds *DatabaseService) GetUserByID(userID int) (*User, error) {
	query := `SELECT id, username, email, created_at, updated_at 
			  FROM users WHERE id = $1`

	user := &User{}
	err := ds.db.QueryRow(query, userID).Scan(
		&user.ID, &user.Username, &user.Email, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	return user, nil
}

func (ds *DatabaseService) SaveCrawlResult(userID int, pageInfo PageInfo) error {
	headingCountsJSON, _ := json.Marshal(pageInfo.HeadingCounts)
	linkAnalysisJSON, _ := json.Marshal(pageInfo.LinkAnalysis)

	query := `INSERT INTO crawl_results 
			  (user_id, url, html_version, title, heading_counts, link_analysis, has_login_form, error, crawled_at) 
			  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	errorStr := ""
	if pageInfo.Error != "" {
		errorStr = pageInfo.Error
	}

	_, err := ds.db.Exec(query, userID, pageInfo.URL, pageInfo.HTMLVersion, pageInfo.Title,
		headingCountsJSON, linkAnalysisJSON, pageInfo.HasLoginForm, errorStr, pageInfo.CrawledAt)
	return err
}

func (ds *DatabaseService) GetCachedCrawlResult(userID int, url string) (*PageInfo, error) {
	query := `SELECT url, html_version, title, heading_counts, link_analysis, has_login_form, error, crawled_at
			  FROM crawl_results 
			  WHERE user_id = $1 AND url = $2 AND crawled_at > $3
			  ORDER BY crawled_at DESC LIMIT 1`

	cacheExpiry := time.Now().Add(-CacheExpiry)

	var headingCountsJSON, linkAnalysisJSON []byte
	var errorStr sql.NullString
	pageInfo := &PageInfo{}

	err := ds.db.QueryRow(query, userID, url, cacheExpiry).Scan(
		&pageInfo.URL, &pageInfo.HTMLVersion, &pageInfo.Title,
		&headingCountsJSON, &linkAnalysisJSON, &pageInfo.HasLoginForm,
		&errorStr, &pageInfo.CrawledAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get cached result: %w", err)
	}

	if errorStr.Valid {
		pageInfo.Error = errorStr.String
	}

	json.Unmarshal(headingCountsJSON, &pageInfo.HeadingCounts)
	json.Unmarshal(linkAnalysisJSON, &pageInfo.LinkAnalysis)

	return pageInfo, nil
}

func (ds *DatabaseService) GetUserCrawlHistory(userID int, limit int) ([]CrawlResult, error) {
	query := `SELECT id, job_id, url, html_version, title, heading_counts, link_analysis, has_login_form, error, crawled_at
			  FROM crawl_results 
			  WHERE user_id = $1 
			  ORDER BY crawled_at DESC 
			  LIMIT $2`

	rows, err := ds.db.Query(query, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get crawl history: %w", err)
	}
	defer rows.Close()

	var results []CrawlResult
	for rows.Next() {
		var result CrawlResult
		var headingCountsJSON, linkAnalysisJSON []byte
		var errorStr sql.NullString
		var jobID sql.NullString

		err := rows.Scan(
			&result.ID, &jobID, &result.URL, &result.HTMLVersion, &result.Title,
			&headingCountsJSON, &linkAnalysisJSON, &result.HasLoginForm,
			&errorStr, &result.CrawledAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan crawl result: %w", err)
		}

		if jobID.Valid {
			result.JobID = jobID.String
		}

		if errorStr.Valid {
			result.Error = errorStr.String
		}

		json.Unmarshal(headingCountsJSON, &result.HeadingCounts)
		json.Unmarshal(linkAnalysisJSON, &result.LinkAnalysis)
		result.UserID = userID

		results = append(results, result)
	}

	return results, nil
}

func (ds *DatabaseService) DeleteCrawlResultsByURLs(userID int, urls []string) error {
	if len(urls) == 0 {
		return nil
	}

	// Create placeholder string for IN clause
	placeholders := make([]string, len(urls))
	args := make([]interface{}, len(urls)+1)
	args[0] = userID

	for i, url := range urls {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = url
	}

	query := fmt.Sprintf(`DELETE FROM crawl_results 
						  WHERE user_id = $1 AND url IN (%s)`,
		strings.Join(placeholders, ","))

	_, err := ds.db.Exec(query, args...)
	return err
}

func (ds *DatabaseService) DeleteCrawlResultsByJobIDs(userID int, jobIDs []string) error {
	if len(jobIDs) == 0 {
		return nil
	}

	// Create placeholder string for IN clause
	placeholders := make([]string, len(jobIDs))
	args := make([]interface{}, len(jobIDs)+1)
	args[0] = userID

	for i, jobID := range jobIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = jobID
	}

	query := fmt.Sprintf(`DELETE FROM crawl_results 
						  WHERE user_id = $1 AND job_id IN (%s)`,
		strings.Join(placeholders, ","))

	_, err := ds.db.Exec(query, args...)
	return err
}

func (ds *DatabaseService) DeleteCrawlResultsByIDs(userID int, resultIDs []int) error {
	if len(resultIDs) == 0 {
		return nil
	}

	// Create placeholder string for IN clause
	placeholders := make([]string, len(resultIDs))
	args := make([]interface{}, len(resultIDs)+1)
	args[0] = userID

	for i, resultID := range resultIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = resultID
	}

	query := fmt.Sprintf(`DELETE FROM crawl_results 
						  WHERE user_id = $1 AND id IN (%s)`,
		strings.Join(placeholders, ","))

	_, err := ds.db.Exec(query, args...)
	return err
}

func (ds *DatabaseService) DeleteJobsByIDs(userID int, jobIDs []string) error {
	if len(jobIDs) == 0 {
		return nil
	}

	// Create placeholder string for IN clause
	placeholders := make([]string, len(jobIDs))
	args := make([]interface{}, len(jobIDs)+1)
	args[0] = userID

	for i, jobID := range jobIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = jobID
	}

	// Delete crawl results first (if not using CASCADE)
	resultQuery := fmt.Sprintf(`DELETE FROM crawl_results 
								WHERE user_id = $1 AND job_id IN (%s)`,
		strings.Join(placeholders, ","))
	_, err := ds.db.Exec(resultQuery, args...)
	if err != nil {
		return err
	}

	// Delete jobs
	jobQuery := fmt.Sprintf(`DELETE FROM crawl_jobs 
							 WHERE user_id = $1 AND id IN (%s)`,
		strings.Join(placeholders, ","))
	_, err = ds.db.Exec(jobQuery, args...)
	return err
}

func (ds *DatabaseService) ClearUserCrawlCache(userID int, urls []string) error {
	if len(urls) == 0 {
		// Clear all cache for user
		query := `DELETE FROM crawl_results WHERE user_id = $1 AND job_id IS NULL`
		_, err := ds.db.Exec(query, userID)
		return err
	}

	// Clear cache for specific URLs
	placeholders := make([]string, len(urls))
	args := make([]interface{}, len(urls)+1)
	args[0] = userID

	for i, url := range urls {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = url
	}

	query := fmt.Sprintf(`DELETE FROM crawl_results 
						  WHERE user_id = $1 AND job_id IS NULL AND url IN (%s)`,
		strings.Join(placeholders, ","))

	_, err := ds.db.Exec(query, args...)
	return err
}

func (ds *DatabaseService) Close() error {
	return ds.db.Close()
}

// Web Crawler Service (same as before)
type WebCrawler struct {
	client *http.Client
	db     *DatabaseService
}

func NewWebCrawler(db *DatabaseService) *WebCrawler {
	return &WebCrawler{
		client: &http.Client{
			Timeout: RequestTimeout,
		},
		db: db,
	}
}

func (wc *WebCrawler) CrawlPage(userID int, targetURL string) PageInfo {
	if cachedResult, err := wc.db.GetCachedCrawlResult(userID, targetURL); err == nil && cachedResult != nil {
		return *cachedResult
	}

	pageInfo := PageInfo{
		URL:       targetURL,
		CrawledAt: time.Now(),
	}

	resp, err := wc.client.Get(targetURL)
	if err != nil {
		pageInfo.Error = fmt.Sprintf("Failed to fetch page: %v", err)
		return pageInfo
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		pageInfo.Error = fmt.Sprintf("HTTP error: %d", resp.StatusCode)
		return pageInfo
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		pageInfo.Error = fmt.Sprintf("Failed to parse HTML: %v", err)
		return pageInfo
	}

	pageInfo.HTMLVersion = wc.extractHTMLVersion(doc)
	pageInfo.Title = wc.extractTitle(doc)
	pageInfo.HeadingCounts = wc.countHeadings(doc)
	pageInfo.LinkAnalysis = wc.analyzeLinksConcurrently(doc, targetURL)
	pageInfo.HasLoginForm = wc.detectLoginForm(doc)

	return pageInfo
}

// Include all crawler helper methods
func (wc *WebCrawler) extractHTMLVersion(doc *goquery.Document) string {
	htmlStr, _ := doc.Html()
	if matched, _ := regexp.MatchString(`(?i)<!DOCTYPE\s+html>`, htmlStr); matched {
		return "HTML5"
	}

	if html := doc.Find("html").First(); html.Length() > 0 {
		xmlns, exists := html.Attr("xmlns")
		if exists && strings.Contains(xmlns, "xhtml") {
			return "XHTML"
		}
	}

	return "HTML5"
}

func (wc *WebCrawler) extractTitle(doc *goquery.Document) string {
	return doc.Find("title").First().Text()
}

func (wc *WebCrawler) countHeadings(doc *goquery.Document) HeadingCount {
	return HeadingCount{
		H1: doc.Find("h1").Length(),
		H2: doc.Find("h2").Length(),
		H3: doc.Find("h3").Length(),
		H4: doc.Find("h4").Length(),
		H5: doc.Find("h5").Length(),
		H6: doc.Find("h6").Length(),
	}
}

func (wc *WebCrawler) analyzeLinksConcurrently(doc *goquery.Document, baseURL string) LinkAnalysis {
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return LinkAnalysis{}
	}

	var links []string
	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists {
			links = append(links, href)
		}
	})

	analysis := LinkAnalysis{
		InaccessibleLinks: make([]InaccessibleLink, 0),
	}
	var wg sync.WaitGroup
	var mu sync.Mutex

	semaphore := make(chan struct{}, MaxWorkers)

	for _, link := range links {
		wg.Add(1)
		go func(link string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			parsedLink, err := url.Parse(link)
			if err != nil {
				return
			}

			if !parsedLink.IsAbs() {
				parsedLink = parsedBase.ResolveReference(parsedLink)
			}

			mu.Lock()
			if parsedLink.Host == parsedBase.Host {
				analysis.InternalLinks++
			} else {
				analysis.ExternalLinks++
			}
			mu.Unlock()

			if errorCode := wc.getLinkErrorCode(parsedLink.String()); errorCode > 0 {
				mu.Lock()
				analysis.InaccessibleLinks = append(analysis.InaccessibleLinks, InaccessibleLink{
					LinkURL:   parsedLink.String(),
					ErrorCode: errorCode,
				})
				mu.Unlock()
			}
		}(link)
	}

	wg.Wait()
	return analysis
}

func (wc *WebCrawler) getLinkErrorCode(link string) int {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Head(link)
	if err != nil {
		resp, err = client.Get(link)
		if err != nil {
			return 0
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return resp.StatusCode
	}

	return 0
}

func (wc *WebCrawler) detectLoginForm(doc *goquery.Document) bool {
	loginPatterns := []string{
		"form[action*='login']",
		"form[action*='signin']",
		"form[action*='auth']",
		"form:has(input[name*='password'])",
		"form:has(input[type='password'])",
		"form:has(input[name*='username'])",
		"form:has(input[name*='email'])",
	}

	for _, pattern := range loginPatterns {
		if doc.Find(pattern).Length() > 0 {
			return true
		}
	}

	return false
}

// JWT Helper Functions
func generateToken(user *User) (string, error) {
	claims := Claims{
		UserID:   user.ID,
		Username: user.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(JWTSecret))
}

func validateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(JWTSecret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, fmt.Errorf("invalid token")
}

// Middleware
func authMiddleware(db *DatabaseService) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenString == authHeader {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Bearer token required"})
			c.Abort()
			return
		}

		claims, err := validateToken(tokenString)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}

		user, err := db.GetUserByID(claims.UserID)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
			c.Abort()
			return
		}

		c.Set("user_id", user.ID)
		c.Set("username", user.Username)
		c.Set("user", user)
		c.Next()
	}
}

func wsAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Query("token")
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token required"})
			c.Abort()
			return
		}

		claims, err := validateToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Next()
	}
}

// HTTP Handlers
func registerHandler(db *DatabaseService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req RegisterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		user, err := db.CreateUser(req.Username, req.Email, req.Password)
		if err != nil {
			if strings.Contains(err.Error(), "duplicate key") {
				c.JSON(http.StatusConflict, gin.H{"error": "Username or email already exists"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
			return
		}

		token, err := generateToken(user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"token": token,
			"user": gin.H{
				"id":       user.ID,
				"username": user.Username,
				"email":    user.Email,
			},
		})
	}
}

func loginHandler(db *DatabaseService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req LoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		user, err := db.GetUserByUsername(req.Username)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
			return
		}

		token, err := generateToken(user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"token": token,
			"user": gin.H{
				"id":       user.ID,
				"username": user.Username,
				"email":    user.Email,
			},
		})
	}
}

// New Enhanced Handlers
func crawlHandler(jobManager *JobManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req CrawlRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID := c.GetInt("user_id")

		// Create job
		job, err := jobManager.CreateJob(userID, req.URLs)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create job"})
			return
		}

		// Start job immediately
		if err := jobManager.StartJob(job.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start job"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"job_id":  job.ID,
			"status":  "started",
			"message": "Crawl job started. Connect to WebSocket for real-time updates.",
		})
	}
}

func jobControlHandler(jobManager *JobManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req JobControlRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID := c.GetInt("user_id")

		switch req.Action {
		case "stop":
			if req.JobID != "" {
				// Stop specific job
				if err := jobManager.StopJob(req.JobID); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusOK, gin.H{"message": "Job stopped"})
			} else {
				// Stop all jobs for user
				if err := jobManager.StopAllJobs(userID); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to stop jobs"})
					return
				}
				c.JSON(http.StatusOK, gin.H{"message": "All jobs stopped"})
			}
		case "start":
			if req.JobID == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Job ID required for start action"})
				return
			}
			if err := jobManager.StartJob(req.JobID); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"message": "Job started"})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid action. Use 'start' or 'stop'"})
		}
	}
}

// Enhanced jobsHandler to include results for current active jobs
func jobsHandler(db *DatabaseService) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt("user_id")

		jobs, err := db.GetCurrentJobsWithResults(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch current jobs"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"jobs": jobs})
	}
}

// func jobsHandler(jobManager *JobManager) gin.HandlerFunc {
// 	return func(c *gin.Context) {
// 		userID := c.GetInt("user_id")
// 		jobs := jobManager.GetUserJobs(userID)
// 		c.JSON(http.StatusOK, gin.H{"jobs": jobs})
// 	}
// }

// Enhanced jobsHistoryHandler to include results for all jobs
func jobsHistoryHandler(db *DatabaseService) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt("user_id")

		jobs, err := db.GetCrawlJobsByUser(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch job history"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"jobs": jobs})
	}
}

// func jobsHistoryHandler(db *DatabaseService) gin.HandlerFunc {
// 	return func(c *gin.Context) {
// 		userID := c.GetInt("user_id")

// 		jobs, err := db.GetCrawlJobsByUser(userID)
// 		if err != nil {
// 			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch job history"})
// 			return
// 		}

// 		c.JSON(http.StatusOK, gin.H{"jobs": jobs})
// 	}
// }

func historyHandler(db *DatabaseService) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt("user_id")

		limitStr := c.DefaultQuery("limit", "50")
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit < 1 || limit > 100 {
			limit = 50
		}

		history, err := db.GetUserCrawlHistory(userID, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch history"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"history": history})
	}
}

func profileHandler(c *gin.Context) {
	user := c.MustGet("user").(*User)
	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":         user.ID,
			"username":   user.Username,
			"email":      user.Email,
			"created_at": user.CreatedAt,
		},
	})
}

func healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
		"time":   time.Now().Unix(),
	})
}

// Re-run analysis handler
func rerunAnalysisHandler(jobManager *JobManager, db *DatabaseService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req RerunRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID := c.GetInt("user_id")

		// Clear existing cache for these URLs
		if err := db.ClearUserCrawlCache(userID, req.URLs); err != nil {
			log.Printf("Warning: Failed to clear cache: %v", err)
		}

		// Create new job for re-analysis
		job, err := jobManager.CreateJob(userID, req.URLs)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create re-analysis job"})
			return
		}

		// Start job immediately
		if err := jobManager.StartJob(job.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start re-analysis job"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"job_id":  job.ID,
			"status":  "started",
			"message": "Re-analysis job started. Previous results cleared from cache.",
			"urls":    req.URLs,
		})
	}
}

// Delete selected data handler
func deleteDataHandler(db *DatabaseService, jobManager *JobManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req DeleteRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID := c.GetInt("user_id")
		var deletedCount int
		var errors []string

		// Delete by URLs
		if len(req.URLs) > 0 {
			if err := db.DeleteCrawlResultsByURLs(userID, req.URLs); err != nil {
				errors = append(errors, fmt.Sprintf("Failed to delete URLs: %v", err))
			} else {
				deletedCount += len(req.URLs)
			}
		}

		// Delete by Job IDs (also stops running jobs)
		if len(req.JobIDs) > 0 {
			// First stop any running jobs
			for _, jobID := range req.JobIDs {
				if err := jobManager.StopJob(jobID); err != nil {
					// Job might not be running, that's okay
					log.Printf("Note: Could not stop job %s: %v", jobID, err)
				}
			}

			if err := db.DeleteJobsByIDs(userID, req.JobIDs); err != nil {
				errors = append(errors, fmt.Sprintf("Failed to delete jobs: %v", err))
			} else {
				deletedCount += len(req.JobIDs)
			}
		}

		// Delete by Result IDs
		if len(req.Results) > 0 {
			if err := db.DeleteCrawlResultsByIDs(userID, req.Results); err != nil {
				errors = append(errors, fmt.Sprintf("Failed to delete results: %v", err))
			} else {
				deletedCount += len(req.Results)
			}
		}

		if len(errors) > 0 {
			c.JSON(http.StatusPartialContent, gin.H{
				"message":       "Partial deletion completed",
				"deleted_count": deletedCount,
				"errors":        errors,
			})
			return
		}

		if deletedCount == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "No valid deletion criteria provided",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":       "Deletion completed successfully",
			"deleted_count": deletedCount,
		})
	}
}

// Clear all history handler
func clearAllHistoryHandler(db *DatabaseService, jobManager *JobManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt("user_id")

		// Stop all running jobs first
		if err := jobManager.StopAllJobs(userID); err != nil {
			log.Printf("Warning: Failed to stop all jobs: %v", err)
		}

		// Delete all crawl results
		query := `DELETE FROM crawl_results WHERE user_id = $1`
		_, err := db.db.Exec(query, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear crawl results"})
			return
		}

		// Delete all jobs
		jobQuery := `DELETE FROM crawl_jobs WHERE user_id = $1`
		_, err = db.db.Exec(jobQuery, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear jobs"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "All crawl history and jobs cleared successfully",
		})
	}
}

// WebSocket Handler
func wsHandler(connMgr *ConnectionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt("user_id")

		conn, err := connMgr.upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Printf("WebSocket upgrade failed: %v", err)
			return
		}

		connMgr.AddConnection(userID, conn)

		// Send welcome message
		connMgr.SendToUser(userID, WSMessage{
			Type: "connected",
			Data: gin.H{"message": "WebSocket connected", "user_id": userID},
		})

		// Handle connection cleanup
		defer func() {
			connMgr.RemoveConnection(userID)
		}()

		// Keep connection alive and handle ping/pong
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		// Read messages (mainly for keeping connection alive)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket error: %v", err)
				}
				break
			}
		}
	}
}

// Main function
func main() {
	// Database connection
	databaseURL := "postgres://postgres:luminous@localhost/webcrawler?sslmode=disable"
	db, err := NewDatabaseService(databaseURL)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}
	defer db.Close()

	// Initialize services
	crawler := NewWebCrawler(db)
	connMgr := NewConnectionManager()
	jobManager := NewJobManager(db, crawler, connMgr)

	// Setup Gin router
	r := gin.Default()

	// CORS middleware
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// Public routes
	r.GET("/health", healthHandler)
	r.POST("/register", registerHandler(db))
	r.POST("/login", loginHandler(db))

	// WebSocket route (with token-based auth)
	r.GET("/ws", wsAuthMiddleware(), wsHandler(connMgr))

	// Protected routes
	protected := r.Group("/api")
	protected.Use(authMiddleware(db))
	{
		protected.GET("/profile", profileHandler)
		protected.POST("/crawl", crawlHandler(jobManager))
		protected.POST("/jobs/control", jobControlHandler(jobManager))
		protected.GET("/jobs", jobsHandler(db))
		protected.GET("/jobs/history", jobsHistoryHandler(db))
		protected.GET("/history", historyHandler(db))

		protected.POST("/rerun", rerunAnalysisHandler(jobManager, db))
		protected.DELETE("/data", deleteDataHandler(db, jobManager))
		protected.DELETE("/history/clear", clearAllHistoryHandler(db, jobManager))
	}

	// Start server
	port := ":8080"
	log.Printf("Server starting on port %s", port)
	if err := r.Run(port); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}

/*
Database Setup:
CREATE DATABASE webcrawler;

Required dependencies:
go mod init web-crawler
go get github.com/gin-gonic/gin
go get github.com/PuerkitoBio/goquery
go get github.com/golang-jwt/jwt/v5
go get github.com/lib/pq
go get golang.org/x/crypto/bcrypt
go get github.com/gorilla/websocket

New Features Usage Examples:

1. Start a crawl job (returns job ID immediately):
curl -X POST http://localhost:8080/api/crawl \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d '{"urls": ["https://example.com", "https://google.com"]}'

2. Stop a specific job:
curl -X POST http://localhost:8080/api/jobs/control \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d '{"job_id": "job_123_1234567890", "action": "stop"}'

3. Stop all jobs for current user:
curl -X POST http://localhost:8080/api/jobs/control \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d '{"action": "stop"}'

4. Start a pending job:
curl -X POST http://localhost:8080/api/jobs/control \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d '{"job_id": "job_123_1234567890", "action": "start"}'

5. Get current jobs status:
curl -X GET http://localhost:8080/api/jobs \
  -H "Authorization: Bearer YOUR_TOKEN_HERE"

6. Get job history:
curl -X GET http://localhost:8080/api/jobs/history \
  -H "Authorization: Bearer YOUR_TOKEN_HERE"

7. WebSocket connection for real-time updates:
Connect to: ws://localhost:8080/ws?token=YOUR_TOKEN_HERE

WebSocket Message Types:
- "connected": Initial connection confirmation
- "job_update": Real-time job progress updates

Example WebSocket Messages:

Job Started:
{
  "type": "job_update",
  "data": {
    "job_id": "job_123_1234567890",
    "status": "running",
    "progress": 0,
    "total_urls": 2
  }
}

URL Processing:
{
  "type": "job_update",
  "data": {
    "job_id": "job_123_1234567890",
    "status": "running",
    "progress": 1,
    "total_urls": 2,
    "current_url": "https://example.com",
    "result": {
      "url": "https://example.com",
      "html_version": "HTML5",
      "title": "Example Domain",
      "heading_counts": {"h1": 1, "h2": 0, "h3": 0, "h4": 0, "h5": 0, "h6": 0},
      "link_analysis": {
        "internal_links": 5,
        "external_links": 3,
        "inaccessible_links": []
      },
      "has_login_form": false,
      "crawled_at": "2024-01-15T10:30:00Z"
    }
  }
}

Job Completed:
{
  "type": "job_update",
  "data": {
    "job_id": "job_123_1234567890",
    "status": "completed",
    "progress": 2,
    "total_urls": 2,
    "completed_at": "2024-01-15T10:32:00Z"
  }
}

Job Stopped:
{
  "type": "job_update",
  "data": {
    "job_id": "job_123_1234567890",
    "status": "stopped",
    "progress": 1,
    "total_urls": 2
  }
}

Frontend Integration Example (JavaScript):

const token = 'YOUR_TOKEN_HERE';
const ws = new WebSocket(`ws://localhost:8080/ws?token=${token}`);

ws.onopen = function() {
    console.log('WebSocket connected');
};

ws.onmessage = function(event) {
    const message = JSON.parse(event.data);

    if (message.type === 'job_update') {
        const jobData = message.data;
        updateJobUI(jobData);
    }
};

function updateJobUI(jobData) {
    // Update progress bar
    const progressPercent = (jobData.progress / jobData.total_urls) * 100;
    document.getElementById('progress').style.width = progressPercent + '%';

    // Update status
    document.getElementById('status').textContent = jobData.status;

    // Show current URL if available
    if (jobData.current_url) {
        document.getElementById('current-url').textContent = jobData.current_url;
    }

    // Display results if available
    if (jobData.result) {
        addResultToTable(jobData.result);
    }
}

function stopJob(jobId) {
    fetch('/api/jobs/control', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({
            job_id: jobId,
            action: 'stop'
        })
    });
}

function stopAllJobs() {
    fetch('/api/jobs/control', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({
            action: 'stop'
        })
    });
}
*/
