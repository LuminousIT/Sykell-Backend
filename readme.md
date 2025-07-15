# Web Crawler API

A powerful, concurrent web crawler API built with Go that analyzes web pages and provides detailed insights including HTML structure, heading counts, link analysis, and login form detection. Features real-time job tracking via WebSockets, user authentication, and comprehensive data management.

## Features

- **Concurrent Web Crawling**: Multi-threaded crawling with configurable worker pools
- **Real-time Updates**: WebSocket support for live job progress tracking
- **User Authentication**: JWT-based authentication with secure user management
- **Comprehensive Analysis**: 
  - HTML version detection
  - Heading count analysis (H1-H6)
  - Internal/external link analysis
  - Inaccessible link detection
  - Login form detection
- **Job Management**: Start, stop, and monitor crawl jobs
- **Data Management**: Re-run analysis and selective deletion of crawl data
- **Caching**: Intelligent result caching to avoid redundant crawls

## Prerequisites

- **Go 1.19+**
- **PostgreSQL 12+**
- **Git**

## Installation & Setup

### 1. Clone the Repository

```bash
git clone <repository-url>
cd web-crawler
```

### 2. Install Dependencies

```bash
go mod init web-crawler
go get github.com/gin-gonic/gin
go get github.com/PuerkitoBio/goquery
go get github.com/golang-jwt/jwt/v5
go get github.com/gorilla/websocket
go get github.com/lib/pq
go get golang.org/x/crypto/bcrypt
```

### 3. Database Setup

Create a PostgreSQL database:

```sql
CREATE DATABASE webcrawler;
```

Update the database connection string in `main.go`:

```go
// Line 1072 in main.go
databaseURL := "postgres://your_username:your_password@localhost/webcrawler?sslmode=disable"
```

The application will automatically create the required tables on first run.

### 4. Security Configuration

**IMPORTANT**: Change the JWT secret in production!

Edit the JWT secret in `main.go`:

```go
// Line 16 in main.go
const (
    JWTSecret = "your-production-secret-key-change-this"
    // ... other constants
)
```

**Security Requirements:**
- Use a strong, unique JWT secret (minimum 32 characters)
- Use HTTPS in production
- Configure proper database credentials
- Set up SSL for database connections in production

### 5. Run the Application

```bash
go run main.go
```

The server will start on port 8080 by default.

## 🔧 Configuration

### Application Constants

You can modify these constants in `main.go` to customize the application:

```go
const (
    JWTSecret      = "your-secret-key-change-this-in-production"
    RequestTimeout = 30 * time.Second  // HTTP request timeout
    MaxWorkers     = 5                 // Concurrent workers for link analysis
    CacheExpiry    = 1 * time.Hour     // Cache crawl results duration
)
```

### Server Port

To change the server port, modify line 1115 in `main.go`:

```go
port := ":8080"  // Change to your desired port
```

### CORS Configuration

CORS is configured to allow all origins by default. For production, modify the CORS middleware in `main.go` (around line 1085):

```go
r.Use(func(c *gin.Context) {
    c.Header("Access-Control-Allow-Origin", "https://your-frontend-domain.com")
    // ... rest of CORS configuration
})
```

## API Documentation

### Authentication

#### Register User
```bash
POST /register
Content-Type: application/json

{
  "username": "testuser",
  "email": "test@example.com", 
  "password": "password123"
}
```

**Response:**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {
    "id": 1,
    "username": "testuser",
    "email": "test@example.com"
  }
}
```

#### Login User
```bash
POST /login
Content-Type: application/json

{
  "username": "testuser",
  "password": "password123"
}
```

### Crawling Operations

#### Start Crawl Job
```bash
POST /api/crawl
Authorization: Bearer <token>
Content-Type: application/json

{
  "urls": ["https://example.com", "https://google.com"]
}
```

**Response:**
```json
{
  "job_id": "job_123_1625097600",
  "status": "started",
  "message": "Crawl job started. Connect to WebSocket for real-time updates."
}
```

#### Get Current Jobs
```bash
GET /api/jobs
Authorization: Bearer <token>
```

#### Get Job History
```bash
GET /api/jobs/history
Authorization: Bearer <token>
```

#### Control Jobs
```bash
POST /api/jobs/control
Authorization: Bearer <token>
Content-Type: application/json

# Stop specific job
{
  "job_id": "job_123_1625097600",
  "action": "stop"
}

# Stop all jobs for user
{
  "action": "stop"
}

# Start pending job
{
  "job_id": "job_123_1625097600", 
  "action": "start"
}
```

#### Get Crawl History
```bash
GET /api/history?limit=50
Authorization: Bearer <token>
```

### Data Management

#### Re-run Analysis
```bash
POST /api/rerun
Authorization: Bearer <token>
Content-Type: application/json

{
  "urls": ["https://example.com"]
}
```

#### Delete Selected Data
```bash
DELETE /api/data
Authorization: Bearer <token>
Content-Type: application/json

{
  "urls": ["https://example.com"],           # Delete by URLs
  "job_ids": ["job_123_1625097600"],         # Delete by Job IDs
  "result_ids": [1, 2, 3]                   # Delete by Result IDs
}
```

#### Clear All History
```bash
DELETE /api/history/clear
Authorization: Bearer <token>
```

### WebSocket Connection

Connect to real-time job updates:

```javascript
const token = 'your-jwt-token';
const ws = new WebSocket(`ws://localhost:8080/ws?token=${token}`);

ws.onopen = function() {
    console.log('WebSocket connected');
};

ws.onmessage = function(event) {
    const message = JSON.parse(event.data);
    
    if (message.type === 'job_update') {
        const jobData = message.data;
        console.log('Job update:', jobData);
        
        // Update UI with progress
        updateProgressBar(jobData.progress, jobData.total_urls);
        
        // Show results if available
        if (jobData.result) {
            displayCrawlResult(jobData.result);
        }
    }
};
```

## Response Examples

### Job with Results Response
```json
{
  "jobs": [
    {
      "id": "job_123_1625097600",
      "user_id": 123,
      "urls": ["https://example.com"],
      "status": "completed",
      "progress": 1,
      "total_urls": 1,
      "created_at": "2024-01-15T10:30:00Z",
      "completed_at": "2024-01-15T10:32:00Z",
      "results": [
        {
          "id": 1,
          "job_id": "job_123_1625097600",
          "url": "https://example.com",
          "html_version": "HTML5",
          "title": "Example Domain",
          "heading_counts": {
            "h1": 1,
            "h2": 0,
            "h3": 0,
            "h4": 0,
            "h5": 0,
            "h6": 0
          },
          "link_analysis": {
            "internal_links": 5,
            "external_links": 3,
            "inaccessible_links": [
              {
                "link_url": "https://broken-link.com",
                "error_code": 404
              }
            ]
          },
          "has_login_form": false,
          "crawled_at": "2024-01-15T10:30:10Z"
        }
      ]
    }
  ]
}
```

### WebSocket Job Update
```json
{
  "type": "job_update",
  "data": {
    "job_id": "job_123_1625097600",
    "status": "running",
    "progress": 1,
    "total_urls": 2,
    "current_url": "https://example.com",
    "result": {
      "url": "https://example.com",
      "title": "Example Domain",
      "html_version": "HTML5",
      "heading_counts": {"h1": 1, "h2": 0, "h3": 0, "h4": 0, "h5": 0, "h6": 0},
      "link_analysis": {
        "internal_links": 5,
        "external_links": 3,
        "inaccessible_links": []
      },
      "has_login_form": false,
      "crawled_at": "2024-01-15T10:30:10Z"
    }
  }
}
```

## Testing

### Health Check
```bash
curl http://localhost:8080/health
```

### Complete Workflow Test
```bash
# 1. Register user
curl -X POST http://localhost:8080/register \
  -H "Content-Type: application/json" \
  -d '{"username":"testuser","email":"test@example.com","password":"password123"}'

# 2. Login and extract token
response=$(curl -X POST http://localhost:8080/login \
  -H "Content-Type: application/json" \
  -d '{"username":"testuser","password":"password123"}')

# Extract token (requires jq)
TOKEN=$(echo $response | jq -r '.token')

# 3. Start crawl job
curl -X POST http://localhost:8080/api/crawl \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"urls":["https://example.com"]}'

# 4. Check job status
curl -X GET http://localhost:8080/api/jobs \
  -H "Authorization: Bearer $TOKEN"

# 5. Get job history
curl -X GET http://localhost:8080/api/jobs/history \
  -H "Authorization: Bearer $TOKEN"
```


## Architecture

### Database Schema

The application creates these tables automatically:

**users**
```sql
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(50) UNIQUE NOT NULL,
    email VARCHAR(100) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

**crawl_jobs**
```sql
CREATE TABLE crawl_jobs (
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
);
```

**crawl_results**
```sql
CREATE TABLE crawl_results (
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
);
```

### Application Structure

```
├── main.go                 # Main application file
├── README.md              # This file
├── go.mod                 # Go module file
└── go.sum                 # Go dependencies checksum
```

### Key Components

- **JobManager**: Manages crawl jobs and their lifecycle
- **WebCrawler**: Handles the actual web crawling logic
- **DatabaseService**: Database operations and connection management
- **ConnectionManager**: WebSocket connection management
- **Authentication**: JWT-based user authentication

## Production Deployment

### Security Checklist

1. **Change JWT Secret**: Replace the default JWT secret
2. **Database Security**: Use strong credentials and SSL
3. **HTTPS**: Deploy with SSL/TLS certificates
4. **CORS**: Restrict allowed origins to your frontend domain
5. **Rate Limiting**: Consider implementing rate limiting
6. **Monitoring**: Set up logging and monitoring

### Example Production Configuration

```go
// Production constants in main.go
const (
    JWTSecret      = "your-very-secure-production-jwt-secret-key-minimum-32-characters"
    RequestTimeout = 45 * time.Second  // Longer timeout for production
    MaxWorkers     = 10                // More workers for production
    CacheExpiry    = 2 * time.Hour     // Longer cache for production
)

// Production database URL
databaseURL := "postgres://prod_user:secure_password@prod-db.example.com:5432/webcrawler_prod?sslmode=require"

// Production CORS
c.Header("Access-Control-Allow-Origin", "https://your-frontend-domain.com")

// Production port
port := ":8080"  // Or read from environment variable
```

### Systemd Service (Linux)

Create `/etc/systemd/system/webcrawler.service`:

```ini
[Unit]
Description=Web Crawler API
After=network.target

[Service]
Type=simple
User=www-data
WorkingDirectory=/opt/webcrawler
ExecStart=/opt/webcrawler/main
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
sudo systemctl enable webcrawler
sudo systemctl start webcrawler
```

## 🐳 Docker Deployment

### Dockerfile

```dockerfile
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY . .
RUN go mod download
RUN go build -o main .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/main .
EXPOSE 8080
CMD ["./main"]
```

### Docker Compose

```yaml
version: '3.8'
services:
  db:
    image: postgres:15
    environment:
      POSTGRES_DB: webcrawler
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: secure_password
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"

  app:
    build: .
    ports:
      - "8080:8080"
    depends_on:
      - db

volumes:
  postgres_data:
```

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License.

## Troubleshooting

### Common Issues

**Database Connection Error**
```
Failed to connect to database: dial tcp: connect: connection refused
```
- Ensure PostgreSQL is running
- Check database credentials in `main.go`
- Verify database exists

**WebSocket Connection Failed**
```
WebSocket upgrade failed: websocket: request origin not allowed
```
- Check CORS configuration
- Ensure proper token is provided in WebSocket URL

**JWT Token Invalid**
```
{"error": "Invalid token"}
```
- Check if token is expired (24-hour expiry)
- Ensure JWT secret matches between login and validation
- Verify token format: `Bearer <token>`

**Permission Denied**
```
failed to create user: duplicate key value violates unique constraint
```
- Username or email already exists
- Use different credentials

### Debug Mode

To enable debug logging, you can modify the Gin mode:

```go
// Add this before r := gin.Default()
gin.SetMode(gin.DebugMode)  // Shows detailed request logs
```

### Health Check Endpoint

Use the health endpoint to verify the service is running:

```bash
curl http://localhost:8080/health
# Expected response: {"status":"healthy","time":1642248000}
```

## Support

For additional support:

1. Check the troubleshooting section above
2. Review the API documentation
3. Ensure all dependencies are properly installed
4. Verify database connectivity and permissions